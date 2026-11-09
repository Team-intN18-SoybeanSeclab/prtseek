// The research runtime gives the model an internal observation ID so it can
// reference real evidence. This annotation is not part of the operator-facing
// tool output, including when an older conversation is replayed.
const internalEvidenceAnnotation = /\[研究证据 evidence_id=t\d+；仅供 research_record\.entry\.evidence_ids 引用，不替代原工具的业务编号\]\s*/g;

export function hideResearchEvidenceAnnotation(value: string): string {
  return value.replace(internalEvidenceAnnotation, "");
}
