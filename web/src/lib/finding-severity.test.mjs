import { filterAndSortFindings } from "./finding-severity.ts";
import assert from "node:assert/strict";
import test from "node:test";

const finding = (id, severity, ts, extra = {}) => ({
  id,
  severity,
  ts,
  vulnclass: "XSS",
  status: "confirmed",
  summary: id,
  evidence: "",
  ...extra,
});

const rows = [
  finding("1", "low", "2026-10-26T12:00:00Z"),
  finding("2", "high", "2026-10-25T12:00:00Z"),
  finding("3", "critical", "2026-10-24T12:00:00Z"),
  finding("4", "medium", "2026-10-26T12:00:00Z"),
  finding("5", "high", "2026-10-26T12:00:00Z", { inherited: true, source_task_id: "source" }),
];

test("all keeps every report and orders severity before newest time", () => {
  assert.deepEqual(
    filterAndSortFindings(rows, "all", "desc").map((row) => row.id),
    ["3", "5", "2", "4", "1"],
  );
});

test("each severity filters reports including inherited reports", () => {
  for (const severity of ["critical", "high", "medium", "low"]) {
    const items = filterAndSortFindings(rows, severity, "desc");
    assert.equal(items.length, severity === "high" ? 2 : 1);
    assert.ok(items.every((row) => row.severity === severity));
  }
  assert.equal(filterAndSortFindings(rows, "high", "desc")[0].source_task_id, "source");
});

test("ascending time changes only the order within a severity", () => {
  assert.deepEqual(
    filterAndSortFindings(rows, "all", "asc").map((row) => row.id),
    ["3", "2", "5", "4", "1"],
  );
});

test("matching severity and timestamp use descending numeric IDs", () => {
  const tied = [finding("2", "high", rows[0].ts), finding("10", "high", rows[0].ts)];
  for (const direction of ["asc", "desc"]) {
    assert.deepEqual(
      filterAndSortFindings(tied, "all", direction).map((row) => row.id),
      ["10", "2"],
    );
  }
});

test("empty input and a severity without matches return an empty list", () => {
  assert.deepEqual(filterAndSortFindings([], "all", "desc"), []);
  assert.deepEqual(filterAndSortFindings([rows[0]], "critical", "desc"), []);
});

test("filtering and sorting preserve the polling snapshot", () => {
  const snapshot = Object.freeze(rows.map((row) => Object.freeze({ ...row })));
  const before = [...snapshot];
  assert.notEqual(filterAndSortFindings(snapshot, "all", "desc"), snapshot);
  filterAndSortFindings(snapshot, "high", "asc");
  assert.deepEqual(snapshot, before);
});

test("keywords match each searchable field as a trimmed, case-insensitive substring", () => {
  for (const field of ["name", "vulnclass", "summary", "evidence"]) {
    const match = finding("match", "high", rows[0].ts, { [field]: "prefix NeEdLe suffix" });
    assert.deepEqual(filterAndSortFindings([rows[0], match], "all", "desc", "  nEEdle  "), [match]);
  }
});

test("unmatched keywords and missing searchable fields return no matches", () => {
  const missing = finding("missing", "high", rows[0].ts, {
    name: undefined,
    vulnclass: undefined,
    summary: undefined,
    evidence: undefined,
  });
  assert.deepEqual(filterAndSortFindings([...rows, missing], "all", "desc", "needle"), []);
  assert.deepEqual(filterAndSortFindings([], "all", "desc", "needle"), []);
});

test("undefined, empty and whitespace keywords preserve severity filtering and sorting", () => {
  for (const query of [undefined, "", "  \t\n  "]) {
    for (const severity of ["all", "high"]) {
      for (const direction of ["asc", "desc"]) {
        assert.deepEqual(
          filterAndSortFindings(rows, severity, direction, query),
          filterAndSortFindings(rows, severity, direction),
        );
      }
    }
  }
});

test("keywords combine with severity and preserve time ordering for inherited reports", () => {
  const matches = rows.map((row) => ({ ...row, evidence: "Shared keyword" }));
  assert.deepEqual(
    filterAndSortFindings(matches, "high", "desc", "KEYWORD").map((row) => row.id),
    ["5", "2"],
  );
  assert.deepEqual(
    filterAndSortFindings(matches, "high", "asc", "keyword").map((row) => row.id),
    ["2", "5"],
  );
  assert.deepEqual(filterAndSortFindings(matches, "high", "desc", "absent"), []);
});
