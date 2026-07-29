import { useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { IssueDetailRoute } from "@multica/views/issues/components";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCanonicalIssueId } from "@multica/core/issues/canonical-id";
import { issueDetailOptions } from "@multica/core/issues/queries";
import { useDocumentTitle } from "@/hooks/use-document-title";

export function IssueDetailPage({ onDelete }: { onDelete?: () => void }) {
  const { id } = useParams<{ id: string }>();
  const wsId = useWorkspaceId();
  // `id` may be an identifier (`MUL-123`); resolve before reading the detail
  // cache so the title watches the same UUID-keyed entry realtime patches.
  const { canonicalId } = useCanonicalIssueId(wsId, id ?? "");
  const { data: issue } = useQuery({
    ...issueDetailOptions(wsId, canonicalId ?? ""),
    enabled: !!canonicalId && !!wsId,
  });

  useDocumentTitle(issue ? `${issue.identifier}: ${issue.title}` : "Issue");

  if (!id) return null;
  // Render errors bubble to the root route errorElement (DesktopRouteErrorPage),
  // which contains the crash inside the tab content pane. No page-level boundary
  // here — a whole-page wrapper duplicates the route-level error UI.
  return <IssueDetailRoute routeId={id} onDelete={onDelete} />;
}
