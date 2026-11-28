export interface ContestSummary {
  key: string;
  title: string;
  category: string;
  source: string;
  fileCount: number;
  savedAt: number;
}

export interface ContestProblem {
  key: string;
  title: string;
  category: string;
  source: string;
  description: string;
  files: File[];
}

function openLibrary(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open("prtseek-contest-library", 1);
    request.onupgradeneeded = () => {
      request.result.createObjectStore("catalog", { keyPath: "key" });
      request.result.createObjectStore("materials", { keyPath: "key" });
    };
    request.onerror = () => reject(request.error);
    request.onsuccess = () => resolve(request.result);
  });
}

async function readLibrary<T>(store: string, read: (store: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  const db = await openLibrary();
  try {
    return await new Promise<T>((resolve, reject) => {
      const req = read(db.transaction(store).objectStore(store));
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
  } finally { db.close(); }
}

export async function contestCatalog() {
  const rows = await readLibrary<ContestSummary[]>("catalog", store => store.getAll());
  return rows.sort((a, b) => b.savedAt - a.savedAt);
}

export function contestMaterial(key: string): Promise<ContestProblem | undefined> {
  return readLibrary("materials", store => store.get(key));
}

// Keep the index and its blobs in the same transaction, including on quota failure.
export async function updateContestLibrary(key: string, item?: ContestProblem) {
  const db = await openLibrary();
  try {
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction(["catalog", "materials"], "readwrite");
      tx.oncomplete = () => resolve();
      tx.onabort = () => reject(tx.error || Error("题库写入失败。"));
      if (item) {
        const { title, category, source } = item;
        tx.objectStore("catalog").put({ key, title, category, source, savedAt: Date.now(), fileCount: item.files.length });
        tx.objectStore("materials").put(item);
      } else {
        tx.objectStore("catalog").delete(key);
        tx.objectStore("materials").delete(key);
      }
    });
  } finally { db.close(); }
}

// The competition window is external input, even after the MessagePort handshake.
export function validateMaterial(data: ContestProblem, origin: string): ContestProblem {
  if (!data || data.source !== origin || typeof data.key !== "string" || data.key.length > 4096 ||
    typeof data.title !== "string" || !data.title.trim() || data.title.length > 240 ||
    typeof data.category !== "string" || data.category.length > 120 ||
    typeof data.description !== "string" || data.description.length > 32000 ||
    !Array.isArray(data.files) || data.files.length > 32 ||
    data.files.some(file => !(file instanceof File) || !file.name || file.name.length > 200 || /[<>:"/\\|?*\x00-\x1f]/.test(file.name)) ||
    data.files.reduce((size, file) => size + file.size, 0) > 64 * 1024 * 1024) {
    throw Error("题面或附件格式无效。");
  }
  return { key: JSON.stringify([origin, data.key]), title: data.title, category: data.category,
    source: origin, description: data.description, files: data.files };
}
