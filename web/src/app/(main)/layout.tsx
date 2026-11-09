"use client";
import { type CSSProperties, type ReactNode, useEffect, useState } from "react";

import { AppSidebar } from "@/app/(main)/_components/sidebar/app-sidebar";
import { PersonalOnboarding } from "@/components/personal-onboarding";
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar";
import { auth } from "@/lib/auth";
import { getClientCookie } from "@/lib/cookie.client";

import { MainContent } from "./_components/main-content";
export default function Layout({ children }: Readonly<{ children: ReactNode }>) {
  const [authed, setAuthed] = useState(false);
  useEffect(() => {
    if (auth.getToken()) setAuthed(true);
    else {
      auth.clearToken();
      window.location.href = "/login";
    }
  }, []);
  const defaultOpen = typeof document === "undefined" ? true : getClientCookie("sidebar_state") !== "false";
  if (!authed) return null;
  return (
    <SidebarProvider
      defaultOpen={defaultOpen}
      style={
        {
          "--sidebar-width": "264px",
          "--sidebar-width-icon": "56px",
          "--dashboard-header-height": "64px",
        } as CSSProperties
      }
    >
      <AppSidebar />
      <PersonalOnboarding />
      <SidebarInset className="min-w-0 overflow-x-hidden">
        <MainContent>{children}</MainContent>
      </SidebarInset>
    </SidebarProvider>
  );
}
