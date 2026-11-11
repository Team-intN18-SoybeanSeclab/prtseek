import assert from "node:assert/strict";
import test from "node:test";
import { uploadChatFiles } from "./chat-attachments.ts";

test("failed message retries reuse uploaded attachments and preserve order", async () => {
  const files = [new File(["one"], "题目.txt"), new File(["two"], "exp.py")];
  let requests = 0;
  const upload = async (batch) => {
    requests++;
    return { attachments: batch.map((file) => ({ name: file.name, size: file.size, path: `uploads/${file.name}` })) };
  };
  const receipts = await uploadChatFiles(files, [], upload);
  const retried = await uploadChatFiles(files, receipts, upload);
  assert.equal(requests, 1);
  assert.deepEqual(retried, receipts);
  assert.deepEqual(await uploadChatFiles([files[1]], retried, upload), [receipts[1]]);
});

test("an upload error leaves the original draft retryable", async () => {
  const files = [new File(["source"], "challenge.bin")];
  await assert.rejects(uploadChatFiles(files, [], async () => { throw new Error("offline"); }), /offline/);
  const receipts = await uploadChatFiles(files, [], async () => ({ attachments: [{ name: "challenge.bin", path: "uploads/challenge.bin", size: 6 }] }));
  assert.equal(receipts[0].file, files[0]);
});
