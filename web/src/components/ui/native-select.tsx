import { ChevronDownIcon } from "lucide-react";
import * as React from "react";

import { cn } from "@/lib/utils";

function NativeSelect({ className, children, ...props }: React.ComponentProps<"select">) {
  return (
    <span data-slot="native-select-wrap" className="relative inline-flex min-w-0 items-center">
      <select
        data-slot="native-select"
        className={cn(
          "h-11 min-w-0 appearance-none border border-input bg-card py-2 pr-10 pl-3 text-base text-foreground outline-none transition-colors focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/40 disabled:cursor-not-allowed disabled:opacity-50",
          className,
        )}
        {...props}
      >
        {children}
      </select>
      <ChevronDownIcon className="pointer-events-none absolute right-3 size-4 text-muted-foreground" aria-hidden="true" />
    </span>
  );
}

export { NativeSelect };
