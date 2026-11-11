"use client";
import type { ReactNode } from "react";

import { usePathname } from "next/navigation";

import { SidebarTrigger } from "@/components/ui/sidebar";
import { sectionFor } from "@/navigation/sidebar/sidebar-items";

import { ThemeSwitcher } from "./sidebar/theme-switcher";

export function MainContent({ children }: { children: ReactNode }) {
  const path = usePathname();
  const chat = path.replace(/\/+$/, "") === "/chat";
  return (
    <>
      <header className="prts-header">
        <SidebarTrigger aria-label="切换导航" />
        {!chat && <strong className="text-sm">{sectionFor(path)[0]}</strong>}
        <div className="order-last max-md:ml-auto">
          <ThemeSwitcher />
        </div>
      </header>
      <div className={chat ? "min-h-0 min-w-0 flex-1" : "prts-page-content"}>{children}</div>
    </>
  );
}
