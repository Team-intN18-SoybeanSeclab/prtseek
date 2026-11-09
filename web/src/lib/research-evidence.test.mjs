import assert from "node:assert/strict";
import test from "node:test";

import { hideResearchEvidenceAnnotation } from "./research-evidence.ts";

test("hides only the internal research evidence annotation", () => {
  const annotation = "[研究证据 evidence_id=t000002；仅供 research_record.entry.evidence_ids 引用，不替代原工具的业务编号] ";
  assert.equal(hideResearchEvidenceAnnotation(`${annotation}真实工具输出`), "真实工具输出");
  assert.equal(hideResearchEvidenceAnnotation(`第一行\n${annotation}第二行`), "第一行\n第二行");
  assert.equal(hideResearchEvidenceAnnotation("flag{fixture} evidence_id=t000002"), "flag{fixture} evidence_id=t000002");
});
