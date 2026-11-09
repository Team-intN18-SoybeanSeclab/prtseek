"use client";
import Link from "next/link";
import { usePathname } from "next/navigation";

import { sidebarItems } from "@/navigation/sidebar/sidebar-items";

import { PrtsBrand } from "@/components/prts-brand";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from "@/components/ui/sidebar";
import { useCurrentUser } from "@/hooks/use-current-user";

import { NavUser } from "./nav-user";

export function AppSidebar(props: React.ComponentProps<typeof Sidebar>) {
  const user = useCurrentUser();
  const path = usePathname().replace(/\/+$/, "");
  const { isMobile, setOpenMobile } = useSidebar();
  return (
    <Sidebar {...props} variant="sidebar" collapsible="icon" className="prts-sidebar">
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton asChild className="prts-brand-link" tooltip="prtseek">
              <Link href="/chat" aria-label="prtseek 研究终端">
                <PrtsBrand />
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>
      <SidebarContent>
        {sidebarItems.map((group) => (
          <section key={group.id} className="prts-nav-group" aria-label={group.label}>
            {group.label && <div className="prts-sidebar-label">{group.label}</div>}
            <div className="px-3">
              <SidebarMenu>
                {group.items
                  .flatMap((item) => item.subItems ?? [item])
                  .map(({ id, title, url, icon: Icon }) => (
                    <SidebarMenuItem key={id}>
                      <SidebarMenuButton asChild isActive={path === url || path.startsWith(`${url}/`)} tooltip={title}>
                        <Link
                          href={url}
                          aria-current={path === url || path.startsWith(`${url}/`) ? "page" : undefined}
                          onClick={() => {
                            if (isMobile) setOpenMobile(false);
                          }}
                        >
                          {Icon && <Icon className="size-4" strokeWidth={1.4} />}
                          <span>{title}</span>
                        </Link>
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  ))}
              </SidebarMenu>
            </div>
          </section>
        ))}
      </SidebarContent>
      <SidebarFooter>
        <NavUser user={user} />
      </SidebarFooter>
    </Sidebar>
  );
}
