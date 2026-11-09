"use client";
import { ThemeSwitcher } from "@/app/(main)/_components/sidebar/theme-switcher";
import { BasicAuthCard } from "@/components/basic-auth-card";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { CHAT_SEND_MODE_OPTIONS, type ChatSendMode, setChatSendMode, useChatSendMode } from "@/lib/chat-send-mode";
export default function SettingsPage() {
  const mode = useChatSendMode();
  return (
    <div className="mx-auto w-full max-w-3xl space-y-6">
      <h1 className="text-xl font-semibold">设置</h1>
      <div className="flex items-center justify-between border p-5">
        <span>界面主题</span>
        <ThemeSwitcher />
      </div>
      <div className="space-y-3 border p-5">
        <Label>发送快捷键</Label>
        <Select value={mode} onValueChange={(value) => setChatSendMode(value as ChatSendMode)}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {CHAT_SEND_MODE_OPTIONS.map((item) => (
              <SelectItem key={item.value} value={item.value}>
                {item.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <BasicAuthCard />
    </div>
  );
}
