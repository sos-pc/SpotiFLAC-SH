import type { ReactNode } from "react";
import { Lock } from "lucide-react";

// Settings that belong to the whole deployment rather than to one account:
// where files land on the shared disk, and the names of shared infrastructure.
// The backend refuses a non-admin's attempt to change them (SplitByScope), and
// this is the screen agreeing with it in advance.
//
// A disabled <fieldset> rather than `disabled` on each control: it disables
// everything nested inside it natively, including the ones added next year by
// someone who never reads this file. It also keeps them focusable-skippable
// without any aria bookkeeping of our own.
//
// The note matters as much as the disabling. A greyed-out field with no
// explanation reads as a bug — the user tries it, nothing happens, and there is
// nowhere to find out why. Saying "your administrator owns this" turns a broken
// control into an understood one.
export function InstanceScoped({
  canEdit,
  children,
  what = "These settings",
}: {
  canEdit: boolean;
  children: ReactNode;
  what?: string;
}) {
  if (canEdit) return <>{children}</>;
  return (
    <fieldset disabled className="m-0 min-w-0 border-0 p-0">
      <p className="mb-3 flex items-start gap-2 rounded-md border border-border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
        <Lock className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
        <span>
          {what} — configured once for this whole SpotiFLAC instance rather
          than per account. Only an administrator can change this.
        </span>
      </p>
      {children}
    </fieldset>
  );
}
