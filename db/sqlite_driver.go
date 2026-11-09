package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"regexp"
	"strings"
	"time"

	"modernc.org/sqlite"
)

// The adapter only normalizes database/sql values and numbered parameters.
// Transactions acquire the SQLite writer reservation at BEGIN, replacing row locks.
type sqliteDriver struct{ base driver.Driver }
type sqliteConn struct{ driver.Conn }

var numberedParameter = regexp.MustCompile(`\$(\d+)`)

func (d *sqliteDriver) Open(name string) (driver.Conn, error) {
	c, e := d.base.Open(name)
	if e != nil {
		return nil, e
	}
	return &sqliteConn{c}, nil
}
func (c *sqliteConn) CheckNamedValue(v *driver.NamedValue) error {
	if v.Value == nil {
		return nil
	}
	if b, ok := v.Value.([]byte); ok {
		v.Value = string(b)
	}
	if s, ok := v.Value.(string); ok && strings.ContainsRune(s, 0) {
		return fmt.Errorf("text contains NUL")
	}
	if t, ok := v.Value.(time.Time); ok {
		v.Value = t.UTC().Format("2006-01-02 15:04:05.999999999")
		return nil
	}
	rv := reflect.ValueOf(v.Value)
	if rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() != reflect.Uint8 {
		if rv.IsNil() {
			v.Value = "[]"
			return nil
		}
		b, e := json.Marshal(v.Value)
		v.Value = string(b)
		return e
	}
	value, e := driver.DefaultParameterConverter.ConvertValue(v.Value)
	if b, ok := value.([]byte); ok {
		value = string(b)
	}
	if t, ok := value.(time.Time); ok {
		value = t.UTC().Format("2006-01-02 15:04:05.999999999")
	}
	v.Value = value
	return e
}
func sqliteQuery(q string) string                           { return numberedParameter.ReplaceAllString(q, "?$1") }
func (c *sqliteConn) Prepare(q string) (driver.Stmt, error) { return c.Conn.Prepare(sqliteQuery(q)) }
func (c *sqliteConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *sqliteConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	r, e := c.Conn.(driver.ExecerContext).ExecContext(ctx, sqliteQuery(q), args)
	if e != nil {
		return nil, fmt.Errorf("SQLite %s: %w", q, e)
	}
	return r, nil
}
func (c *sqliteConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	r, e := c.Conn.(driver.QueryerContext).QueryContext(ctx, sqliteQuery(q), args)
	if e != nil {
		return nil, fmt.Errorf("SQLite %s: %w", q, e)
	}
	return &sqliteRows{r}, nil
}

type sqliteRows struct{ driver.Rows }

func (r *sqliteRows) ColumnTypeDatabaseTypeName(i int) string {
	if typed, ok := r.Rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
		return typed.ColumnTypeDatabaseTypeName(i)
	}
	return ""
}
func (r *sqliteRows) Next(dest []driver.Value) error {
	if e := r.Rows.Next(dest); e != nil {
		return e
	}
	for i, v := range dest {
		if s, ok := v.(string); ok {
			parsed := false
			if typ := r.ColumnTypeDatabaseTypeName(i); typ == "DATETIME" || (typ == "" && isSQLiteTimeColumn(r.Columns()[i])) {
				for _, layout := range []string{"2006-01-02 15:04:05.999999999", time.RFC3339Nano} {
					if t, e := time.Parse(layout, s); e == nil {
						dest[i] = t
						parsed = true
						break
					}
				}
			}
			if !parsed {
				dest[i] = []byte(s)
			}
		}
	}
	return nil
}
func jsonCollection(v driver.Value) []any {
	var out []any
	if v != nil {
		_ = json.Unmarshal([]byte(sqlText(v)), &out)
	}
	return out
}
func sqlText(v any) string {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return fmt.Sprint(v)
}
func init() {
	base, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		panic(err)
	}
	sql.Register("prtseek-sqlite", &sqliteDriver{base: base.Driver()})
	base.Close()
	register := func(name string, n int32, f func([]driver.Value) (driver.Value, error)) {
		sqlite.MustRegisterDeterministicScalarFunction(name, n, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) { return f(a) })
	}
	// Queue order must survive equal clock ticks and clock adjustments. The MAX
	// is read by the same UPDATE that reserves the SQLite writer lock.
	register("next_queue_time", 2, func(a []driver.Value) (driver.Value, error) {
		const layout = "2006-01-02 15:04:05.999999999"
		next, err := time.Parse(layout, sqlText(a[0]))
		if err != nil {
			return nil, err
		}
		if a[1] != nil {
			last, err := time.Parse(layout, sqlText(a[1]))
			if err != nil {
				return nil, err
			}
			if !next.After(last) {
				next = last.Add(time.Nanosecond)
			}
		}
		return next.Format(layout), nil
	})
	register("collection_union", 2, func(a []driver.Value) (driver.Value, error) {
		out := []any{}
		seen := map[string]bool{}
		for _, v := range append(jsonCollection(a[0]), jsonCollection(a[1])...) {
			b, _ := json.Marshal(v)
			if !seen[string(b)] {
				seen[string(b)] = true
				out = append(out, v)
			}
		}
		b, e := json.Marshal(out)
		return string(b), e
	})
	register("collection_merge", 3, func(a []driver.Value) (driver.Value, error) {
		out := []any{}
		positions := map[string]int{}
		keys := strings.Split(fmt.Sprint(a[2]), ",")
		for _, v := range append(jsonCollection(a[0]), jsonCollection(a[1])...) {
			if str, ok := v.(string); ok {
				var obj any
				if json.Unmarshal([]byte(str), &obj) == nil {
					v = obj
				}
			}
			b, _ := json.Marshal(v)
			key := string(b)
			if obj, ok := v.(map[string]any); ok && keys[0] != "" {
				parts := []any{}
				for _, k := range keys {
					parts = append(parts, obj[k])
				}
				b, _ = json.Marshal(parts)
				key = string(b)
			}
			if pos, ok := positions[key]; ok {
				out[pos] = v
			} else {
				positions[key] = len(out)
				out = append(out, v)
			}
		}
		b, e := json.Marshal(out)
		return string(b), e
	})
	register("masklen", 1, func(a []driver.Value) (driver.Value, error) {
		p, e := netip.ParsePrefix(fmt.Sprint(a[0]))
		if e != nil {
			return nil, nil
		}
		return int64(p.Bits()), nil
	})
	register("regexp_replace", 4, func(a []driver.Value) (driver.Value, error) {
		pattern := strings.ReplaceAll(fmt.Sprint(a[1]), "[[:space:]]", `\s`)
		re, e := regexp.Compile(pattern)
		if e != nil {
			return nil, e
		}
		return re.ReplaceAllString(fmt.Sprint(a[0]), fmt.Sprint(a[2])), nil
	})
	register("collection_append", 2, func(a []driver.Value) (driver.Value, error) {
		b, e := json.Marshal(append(jsonCollection(a[0]), a[1]))
		return string(b), e
	})
	register("collection_remove", 2, func(a []driver.Value) (driver.Value, error) {
		out := []any{}
		for _, v := range jsonCollection(a[0]) {
			if fmt.Sprint(v) != fmt.Sprint(a[1]) {
				out = append(out, v)
			}
		}
		b, e := json.Marshal(out)
		return string(b), e
	})
	register("cidr_contains", 2, func(a []driver.Value) (driver.Value, error) {
		p, e := netip.ParsePrefix(fmt.Sprint(a[0]))
		if e != nil {
			return int64(0), nil
		}
		ip, e := netip.ParseAddr(fmt.Sprint(a[1]))
		if e != nil {
			p2, e2 := netip.ParsePrefix(fmt.Sprint(a[1]))
			if e2 != nil {
				return int64(0), nil
			}
			ip = p2.Addr()
			if p2.Bits() < p.Bits() {
				return int64(0), nil
			}
		}
		if p.Contains(ip) {
			return int64(1), nil
		}
		return int64(0), nil
	})
	register("try_inet", 1, func(a []driver.Value) (driver.Value, error) {
		if _, e := netip.ParseAddr(fmt.Sprint(a[0])); e != nil {
			return nil, nil
		}
		return a[0], nil
	})
	register("concat_ws", -1, func(a []driver.Value) (driver.Value, error) {
		parts := []string{}
		for _, v := range a[1:] {
			if v != nil {
				parts = append(parts, fmt.Sprint(v))
			}
		}
		return strings.Join(parts, fmt.Sprint(a[0])), nil
	})
}

// Aggregated timestamps have no declared column type.
func isSQLiteTimeColumn(name string) bool {
	return strings.HasSuffix(name, "_at") || name == "ts" || name == "last_used"
}
