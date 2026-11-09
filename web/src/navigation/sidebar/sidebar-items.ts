import { Bot, FolderOpen, type LucideIcon, Network, Settings2, Sparkles, Wrench } from "lucide-react";

type NavBadge = "new" | "soon";

interface NavSubItem {
  id: string;
  title: string;
  url: string;
  icon?: LucideIcon;
  badge?: NavBadge;
  disabled?: boolean;
  newTab?: boolean;
}

interface NavItemBase {
  id: string;
  title: string;
  icon?: LucideIcon;
  badge?: NavBadge;
  disabled?: boolean;
  newTab?: boolean;
}

interface NavMainLinkItem extends NavItemBase {
  url: string;
  subItems?: never;
}

interface NavMainParentItem extends NavItemBase {
  subItems: NavSubItem[];
}

type NavMainItem = NavMainLinkItem | NavMainParentItem;

export interface NavGroup {
  id: number;
  label?: string;
  items: NavMainItem[];
}

export const sidebarItems: NavGroup[] = [
  {
    id: 1,
    items: [
      { id: "chat", title: "对话与会话", url: "/chat", icon: Bot },
      { id: "agents", title: "Agents 管理", url: "/system/agents", icon: Bot },
      { id: "llm", title: "LLM 管理", url: "/system/llm", icon: Network },
      { id: "tools", title: "工具管理", url: "/system/tools", icon: Wrench },
      { id: "settings", title: "设置", url: "/settings", icon: Settings2 },
      { id: "workspace", title: "文件工作区", url: "/function/workspace", icon: FolderOpen },
      { id: "skills", title: "技能库", url: "/system/skills", icon: Sparkles },
    ],
  },
];

export function sectionFor(path: string): [string, string] {
  const links = sidebarItems.flatMap((group) => group.items.flatMap((item) => item.subItems ?? [item]));
  const match = links
    .filter((item) => path === item.url || path.startsWith(`${item.url}/`))
    .sort((a, b) => b.url.length - a.url.length)[0];
  return match ? [match.title, `PRTSEEK / ${match.id.toUpperCase()}`] : ["对话", "PRTSEEK"];
}
