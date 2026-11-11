"use client";

import { type CSSProperties, useEffect, useRef, useState } from "react";

import { PrtsMark } from "@/components/prts-brand";

const BOOT_DURATION_MS = 2000;

export function PrtsBoot() {
  // Play once per document load; navigation inside the app keeps this layout mounted.
  const [visible, setVisible] = useState(true);
  const dialogRef = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const dialog = dialogRef.current;
    // The server-rendered overlay already covers the page. Make it modal after
    // hydration so keyboard focus cannot reach the workspace behind it.
    if (dialog) {
      dialog.close();
      dialog.showModal();
      dialog.dataset.playing = "true";
    }
    const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    // A timer also dismisses the introduction if CSS animations are interrupted.
    const timer = window.setTimeout(() => setVisible(false), reducedMotion ? 600 : BOOT_DURATION_MS);
    return () => window.clearTimeout(timer);
  }, []);

  if (!visible) return null;

  return (
    <dialog
      ref={dialogRef}
      open
      className="prts-boot"
      style={{ "--prts-boot-duration": `${BOOT_DURATION_MS}ms` } as CSSProperties}
      aria-label="prtseek 开机动画"
      onAnimationEnd={(event) => {
        if (event.target === event.currentTarget) setVisible(false);
      }}
      onCancel={(event) => {
        event.preventDefault();
        setVisible(false);
      }}
    >
      <div className="prts-boot-emblem" aria-hidden="true">
        <PrtsMark className="prts-boot-mark" />
        <span>PRTS / INITIALIZING</span>
      </div>
      <div className="prts-boot-wordmark" aria-hidden="true">
        <strong>prtseek</strong>
        <span className="prts-boot-os">ANALYSIS OS</span>
        <span className="prts-boot-rule" />
        <span className="prts-boot-caption">PERSONAL RESEARCH TERMINAL</span>
      </div>
      <span className="prts-boot-index" aria-hidden="true">
        SYSTEM / 01
      </span>
      <span className="prts-boot-progress" aria-hidden="true" />
      <button type="button" onClick={() => setVisible(false)} className="prts-boot-skip">
        跳过 / SKIP
      </button>
    </dialog>
  );
}
