"use client";

import Link from "next/link";

import { PrtsBrand } from "@/components/prts-brand";
import { Button } from "@/components/ui/button";

export default function NotFound() {
  return (
    <main className="flex min-h-dvh flex-col items-center justify-center gap-6 px-6 text-center">
      <PrtsBrand />
      <div className="prts-section-code">404 / LOCATION NOT FOUND</div>
      <h1 className="font-semibold text-2xl">未找到此页面</h1>
      <p className="text-muted-foreground text-sm">页面可能已经移动，返回研究终端继续工作。</p>
      <Link prefetch={false} replace href="/chat">
        <Button variant="outline">返回研究终端 ↗</Button>
      </Link>
    </main>
  );
}
