import type { ChatAttachment } from "./types";

export interface PendingChat {
  input?: string;
  files?: File[];
  uploads?: UploadedFile[];
}

export interface UploadedFile {
  file: File;
  attachment: ChatAttachment;
}

// Preserve upload receipts when sending fails, including the draft -> session
// transition. Retrying a message must not upload another copy of the same file.
export async function uploadChatFiles(
  files: File[],
  receipts: UploadedFile[],
  upload: (files: File[]) => Promise<{ attachments: ChatAttachment[] }>,
): Promise<UploadedFile[]> {
  const missing = files.filter((file) => !receipts.some((item) => item.file === file));
  const known = receipts.filter((item) => files.includes(item.file));
  if (missing.length) {
    const result = await upload(missing);
    if (result.attachments.length !== missing.length) throw new Error("附件上传结果不完整，请重试。");
    known.push(...missing.map((file, index) => ({ file, attachment: result.attachments[index] })));
  }
  return files.map((file) => known.find((item) => item.file === file)!);
}
