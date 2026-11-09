"use client";
import { ModelEditor } from "@/components/model-settings";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useEffect, useState } from "react";

export function PersonalOnboarding() {
  const [open, setOpen] = useState(false);
  useEffect(() => {
    const configure = () => setOpen(true);
    window.addEventListener("prtseek-configure-model", configure);
    return () => window.removeEventListener("prtseek-configure-model", configure);
  }, []);
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>配置模型</DialogTitle>
          <DialogDescription>填写模型配置后即可开始对话。</DialogDescription>
        </DialogHeader>
        <ModelEditor initial onSaved={() => setOpen(false)} />
      </DialogContent>
    </Dialog>
  );
}
