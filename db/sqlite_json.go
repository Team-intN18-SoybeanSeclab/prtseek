package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var identifierRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// JSON archives use column metadata rather than database-specific row conversion.
type archiveJSONRows struct {
	*sql.Rows
	columns []string
	types   []*sql.ColumnType
}

func newArchiveJSONRows(rows *sql.Rows) *archiveJSONRows {
	cols, _ := rows.Columns()
	types, _ := rows.ColumnTypes()
	return &archiveJSONRows{rows, cols, types}
}
func (r *archiveJSONRows) Scan(dest ...any) error {
	vals := make([]any, len(r.columns))
	ptrs := make([]any, len(vals))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if e := r.Rows.Scan(ptrs...); e != nil {
		return e
	}
	row := map[string]any{}
	for i, col := range r.columns {
		v := vals[i]
		if b, ok := v.([]byte); ok {
			v = string(b)
		}
		if s, ok := v.(string); ok && i < len(r.types) && r.types[i].DatabaseTypeName() == "JSONTEXT" {
			var j any
			if err := json.Unmarshal([]byte(s), &j); err != nil {
				return err
			}
			v = j
		}
		row[col] = v
	}
	b, e := json.Marshal(row)
	if e != nil {
		return e
	}
	if p, ok := dest[0].(*[]byte); ok {
		*p = b
		return nil
	}
	return fmt.Errorf("archive row expects *[]byte")
}

func sqliteArchiveValue(v any, dateTime bool) any {
	switch x := v.(type) {
	case map[string]any, []any:
		b, _ := json.Marshal(x)
		return string(b)
	case json.Number:
		if n, e := x.Int64(); e == nil {
			return n
		}
		f, _ := x.Float64()
		return f
	case string:
		if t, e := time.Parse(time.RFC3339Nano, x); dateTime && e == nil {
			return t.UTC().Format("2006-01-02 15:04:05.999999999")
		}
	}
	return v
}
func insertSQLiteRows(tx *sql.Tx, table string, raw json.RawMessage, ignore bool) (sql.Result, error) {
	rows, e := decodeArchiveRows(raw)
	if e != nil {
		return nil, e
	}
	types, e := sqliteColumnTypes(tx, table)
	if e != nil {
		return nil, e
	}
	var result sql.Result
	// Table and column names must exist in our embedded schema, not archive-controlled SQL.
	for _, row := range rows {
		cols := []string{}
		for k := range row {
			if !identifierRE.MatchString(k) {
				return nil, fmt.Errorf("invalid archive column")
			}
			cols = append(cols, k)
		}
		sort.Strings(cols)
		if !identifierRE.MatchString(table) {
			return nil, fmt.Errorf("invalid archive table")
		}
		args := []any{}
		marks := []string{}
		quoted := []string{}
		for _, k := range cols {
			quoted = append(quoted, `"`+k+`"`)
			marks = append(marks, "?")
			args = append(args, sqliteArchiveValue(row[k], types[k] == "DATETIME"))
		}
		suffix := ""
		if ignore {
			suffix = " ON CONFLICT DO NOTHING"
		}
		if table == "task_asset_links" {
			updates := []string{}
			for _, k := range cols {
				if k != "task_id" && k != "asset_id" {
					updates = append(updates, `"`+k+`"=excluded."`+k+`"`)
				}
			}
			suffix = " ON CONFLICT(task_id,asset_id) DO UPDATE SET " + strings.Join(updates, ",")
		}
		result, e = tx.Exec(`INSERT INTO "`+table+`" (`+strings.Join(quoted, ",")+") VALUES ("+strings.Join(marks, ",")+")"+suffix, args...)
		if e != nil {
			return nil, e
		}
	}
	return result, nil
}
func updateSQLiteRow(tx *sql.Tx, table string, id int64, row map[string]any) error {
	types, err := sqliteColumnTypes(tx, table)
	if err != nil {
		return err
	}
	keys := []string{}
	for k := range row {
		if k != "id" {
			if !identifierRE.MatchString(k) {
				return fmt.Errorf("invalid archive column")
			}
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	sets := []string{}
	args := []any{}
	for _, k := range keys {
		sets = append(sets, `"`+k+`"=?`)
		args = append(args, sqliteArchiveValue(row[k], types[k] == "DATETIME"))
	}
	args = append(args, id)
	_, e := tx.Exec(`UPDATE "`+table+`" SET `+strings.Join(sets, ",")+" WHERE id=?", args...)
	return e
}

func sqliteColumnTypes(tx *sql.Tx, table string) (map[string]string, error) {
	if !identifierRE.MatchString(table) {
		return nil, fmt.Errorf("invalid archive table")
	}
	rows, err := tx.Query(`SELECT name,type FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	types := map[string]string{}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		types[name] = strings.ToUpper(typ)
	}
	return types, rows.Err()
}
