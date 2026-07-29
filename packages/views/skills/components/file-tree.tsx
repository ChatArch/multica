"use client";

import { useState } from "react";
import {
  ChevronRight,
  ChevronDown,
  FileText,
  File,
  Folder,
  FolderOpen,
} from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

// ---------------------------------------------------------------------------
// Tree data structures
// ---------------------------------------------------------------------------

interface FileTreeNode {
  name: string;
  path: string;
  isDirectory: boolean;
  children: FileTreeNode[];
}

function buildTree(filePaths: string[]): FileTreeNode[] {
  const root: FileTreeNode[] = [];

  for (const filePath of filePaths) {
    const parts = filePath.split("/");
    let current = root;

    for (let i = 0; i < parts.length; i++) {
      const name = parts[i]!;
      const isLast = i === parts.length - 1;
      const path = parts.slice(0, i + 1).join("/");

      let existing = current.find((n) => n.name === name);

      if (!existing) {
        existing = {
          name,
          path,
          isDirectory: !isLast,
          children: [],
        };
        current.push(existing);
      }

      if (!isLast) {
        current = existing.children;
      }
    }
  }

  function sortNodes(nodes: FileTreeNode[]): FileTreeNode[] {
    nodes.sort((a, b) => {
      if (a.path === "SKILL.md") return -1;
      if (b.path === "SKILL.md") return 1;
      if (a.isDirectory !== b.isDirectory) return a.isDirectory ? -1 : 1;
      return a.name.localeCompare(b.name);
    });
    for (const node of nodes) {
      if (node.isDirectory) sortNodes(node.children);
    }
    return nodes;
  }

  return sortNodes(root);
}

function getFileIcon(name: string) {
  if (name.endsWith(".md") || name.endsWith(".mdx")) return FileText;
  return File;
}

// ---------------------------------------------------------------------------
// Tree node renderer
// ---------------------------------------------------------------------------

function TreeNodeItem({
  node,
  selectedPath,
  onSelect,
  depth = 0,
}: {
  node: FileTreeNode;
  selectedPath: string;
  onSelect: (path: string) => void;
  depth?: number;
}) {
  const [expanded, setExpanded] = useState(true);
  const isSelected = node.path === selectedPath;

  if (node.isDirectory) {
    const FolderIcon = expanded ? FolderOpen : Folder;
    const ChevronIcon = expanded ? ChevronDown : ChevronRight;

    return (
      <div>
        <button
          type="button"
          onClick={() => setExpanded(!expanded)}
          className="flex h-8 w-full items-center gap-1.5 rounded-md pr-2.5 text-left text-caption text-muted-foreground transition-colors hover:bg-surface-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          style={{ paddingLeft: `${depth * 12 + 10}px` }}
        >
          <ChevronIcon className="h-3 w-3 shrink-0" />
          <FolderIcon className="h-3.5 w-3.5 shrink-0" />
          <span className="truncate">{node.name}</span>
        </button>
        {expanded && (
          <div>
            {node.children.map((child) => (
              <TreeNodeItem
                key={child.path}
                node={child}
                selectedPath={selectedPath}
                onSelect={onSelect}
                depth={depth + 1}
              />
            ))}
          </div>
        )}
      </div>
    );
  }

  const Icon = getFileIcon(node.name);

  return (
    <button
      type="button"
      role="tab"
      aria-selected={isSelected}
      onClick={() => onSelect(node.path)}
      className={cn(
        "flex h-8 w-full items-center gap-2 rounded-md pr-2.5 text-left text-caption transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        isSelected
          ? "bg-surface-selected font-medium text-surface-selected-foreground"
          : "text-muted-foreground hover:bg-surface-hover hover:text-foreground",
      )}
      style={{ paddingLeft: `${depth * 12 + 10}px` }}
    >
      <Icon className="h-3.5 w-3.5 shrink-0" />
      <span className="truncate">{node.name}</span>
    </button>
  );
}

// ---------------------------------------------------------------------------
// Public component
// ---------------------------------------------------------------------------

export function FileTree({
  filePaths,
  selectedPath,
  onSelect,
}: {
  filePaths: string[];
  selectedPath: string;
  onSelect: (path: string) => void;
}) {
  const { t } = useT("skills");
  const tree = buildTree(filePaths);

  if (tree.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-8 text-muted-foreground">
        <FolderOpen className="h-5 w-5 text-muted-foreground/40" />
        <p className="mt-2 text-caption">{t(($) => $.file_tree.no_files)}</p>
      </div>
    );
  }

  // No `role="tablist"` here: the caller owns the list semantics so a rail
  // split into "main" + "supporting" groups stays a single tab list.
  return (
    <div className="flex flex-col gap-0.5">
      {tree.map((node) => (
        <TreeNodeItem
          key={node.path}
          node={node}
          selectedPath={selectedPath}
          onSelect={onSelect}
        />
      ))}
    </div>
  );
}
