"use client";

import { useState } from "react";
import { Braces, Check } from "lucide-react";
import { extractBuilderDraftBlock } from "@multica/core/agents";
import type { ChatMessage } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { useT } from "../../i18n";

/**
 * The structured payload a builder reply carries, rendered as an affordance
 * rather than prose.
 *
 * Every reply ends in an `<agent_draft>` block that rewrites the form on the
 * right. Left in the transcript it is a wall of JSON; deleted outright it left
 * the user watching fields change with no stated cause. A full-width row states
 * that the configuration moved, and opens the exact payload for anyone who
 * wants to check what the builder actually said.
 *
 * Only the settled reply gets this. Mid-stream the block is still being written
 * — there is no complete payload to show — so the transcript shows an
 * "updating" line instead, via the same strip that hides the half-written JSON.
 */
export function AgentDraftBlock({ message }: { message: ChatMessage }) {
  const { t } = useT("agents");
  const [open, setOpen] = useState(false);

  const json = extractBuilderDraftBlock(message.content);
  if (!json) return null;

  return (
    <>
      <Button
        type="button"
        variant="outline"
        className="h-auto w-full justify-start gap-2 px-3 py-2 text-caption font-normal text-muted-foreground hover:text-foreground"
        onClick={() => setOpen(true)}
      >
        <Check className="size-3.5 shrink-0 text-success" aria-hidden="true" />
        <span className="min-w-0 flex-1 truncate text-left">
          {t(($) => $.creation_studio.builder.draft_updated)}
        </span>
        <Braces className="size-3.5 shrink-0" aria-hidden="true" />
      </Button>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>
              {t(($) => $.creation_studio.builder.draft_json_title)}
            </DialogTitle>
            <DialogDescription>
              {t(($) => $.creation_studio.builder.draft_json_hint)}
            </DialogDescription>
          </DialogHeader>
          <pre className="max-h-[60vh] overflow-auto rounded-lg bg-muted p-4 text-label leading-6">
            <code>{json}</code>
          </pre>
        </DialogContent>
      </Dialog>
    </>
  );
}
