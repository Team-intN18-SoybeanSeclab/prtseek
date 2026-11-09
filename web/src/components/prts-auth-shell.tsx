import type { ReactNode } from "react";

import { PrtsBrand, PrtsMark } from "@/components/prts-brand";

export function PrtsAuthShell({ children, mode = "ACCESS" }: { children: ReactNode; mode?: string }) {
  return (
    <main className="prts-auth-stage">
      <div className="prts-auth-coordinates" aria-hidden="true">
        PRTSEEK / LOCAL OPERATIONS SYSTEM
      </div>
      <div className="prts-auth">
        <aside className="prts-auth-identity" aria-label="prtseek">
          <div className="prts-auth-top">
            <PrtsBrand />
            <span className="prts-micro">PERSONAL TERMINAL</span>
          </div>
          <div className="prts-auth-symbol">
            <PrtsMark />
            <span>prtseek</span>
            <i>ANALYSIS OS</i>
          </div>
          <div className="prts-auth-caption">
            <span>观察。推演。验证。</span>
            <p>OBSERVE / REASON / VERIFY</p>
          </div>
          <div className="prts-auth-bottom">
            <span>RESEARCH SYSTEM</span>
            <span>01 — {mode}</span>
          </div>
        </aside>
        <section className="prts-auth-form">
          <div className="prts-auth-mobile">
            <PrtsBrand />
          </div>
          <div className="prts-auth-content">
            <div className="prts-section-code">SYSTEM / {mode}</div>
            {children}
          </div>
          <footer className="prts-auth-foot">
            <span>prtseek</span>
            <span>PERSONAL RESEARCH TERMINAL</span>
          </footer>
        </section>
      </div>
    </main>
  );
}
