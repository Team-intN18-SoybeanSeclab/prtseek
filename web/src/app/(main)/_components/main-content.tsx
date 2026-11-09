"use client";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { sectionFor } from "@/navigation/sidebar/sidebar-items";
import { usePathname } from "next/navigation";
import type { ReactNode } from "react";

export function MainContent({ children }: { children: ReactNode }) {
  const path = usePathname();
  const chat = path.replace(/\/+$/, "") === "/chat";
  return (
    <>
      <header className="prts-header">
        <SidebarTrigger aria-label="切换导航" />
        {!chat && <strong className="text-sm">{sectionFor(path)[0]}</strong>}
      </header>
      <div className={chat ? "min-h-0 min-w-0 flex-1" : "prts-page-content"}>{children}</div>
    </>
  );
}
