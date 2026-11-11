import assert from "node:assert/strict";
import test from "node:test";
import { executionLabel, outcomeLabel } from "./ctf-status.ts";

test("execution completion is separate from verified completion", () => {
  assert.equal(executionLabel("completed"), "运行结束");
  assert.equal(outcomeLabel("unverified"), "目标待验证");
  assert.equal(outcomeLabel("blocked"), "目标受阻");
  assert.equal(executionLabel("interrupted"), "已中断");
  assert.equal(executionLabel("timed_out"), "已超时");
});
