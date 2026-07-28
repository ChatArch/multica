import { Extension } from "@tiptap/core";
import { Plugin, PluginKey, TextSelection } from "@tiptap/pm/state";
import type { UploadResult } from "@multica/core/hooks/use-file-upload";
import { createSafeId } from "@multica/core/utils";

/** Find and remove a fileCard node by uploadId. */
 
function removeUploadingFileCard(editor: any, uploadId: string) {
  const { tr } = editor.state;
  let deleted = false;
  editor.state.doc.descendants((node: any, pos: number) => {
    if (deleted) return false;
    if (node.type.name === "fileCard" && node.attrs.uploadId === uploadId) {
      tr.delete(pos, pos + node.nodeSize);
      deleted = true;
      return false;
    }
    return undefined;
  });
  if (deleted) editor.view.dispatch(tr);
}

/** Update a fileCard node from uploading state to final state with real URL. */
 
function finalizeFileCard(editor: any, uploadId: string, href: string) {
  const { tr } = editor.state;
  let updated = false;
  editor.state.doc.descendants((node: any, nodePos: number) => {
    if (updated) return false;
    if (node.type.name === "fileCard" && node.attrs.uploadId === uploadId) {
      tr.setNodeMarkup(nodePos, undefined, {
        ...node.attrs,
        href,
        uploading: false,
      });
      updated = true;
      return false;
    }
    return undefined;
  });
  if (updated) editor.view.dispatch(tr);
}

export function findImagePosBySrc(editor: any, src: string): number | null {
  if (!editor) return null;
  let imagePos: number | null = null;
  editor.state.doc.descendants((node: any, pos: number) => {
    if (imagePos !== null) return false;
    if (node.type.name === "image" && node.attrs.src === src) {
      imagePos = pos;
      return false;
    }
    return undefined;
  });
  return imagePos;
}

function removeImageBySrc(editor: any, src: string) {
  const imagePos = findImagePosBySrc(editor, src);
  if (imagePos === null) return;

  const imageNode = editor.state.doc.nodeAt(imagePos);
  if (!imageNode) return;

  const tr = editor.state.tr.delete(imagePos, imagePos + imageNode.nodeSize);
  editor.view.dispatch(tr);
}

/**
 * Read an image's intrinsic pixel dimensions off-thread. Returns null when the
 * decode fails or the API is unavailable (e.g. jsdom in tests, where
 * `createImageBitmap` is undefined) — callers degrade to no reserved box.
 */
async function readImageDimensions(
  file: File,
): Promise<{ width: number; height: number } | null> {
  if (typeof createImageBitmap !== "function") return null;
  try {
    const bitmap = await createImageBitmap(file);
    const dims = { width: bitmap.width, height: bitmap.height };
    bitmap.close();
    return dims.width > 0 && dims.height > 0 ? dims : null;
  } catch {
    return null;
  }
}

/**
 * Measure the file's intrinsic size and write it onto the freshly-inserted
 * image node so the browser reserves the box before decode (no layout shift).
 * Fire-and-forget after insert: keyed on the blob `src`, so if the upload swap
 * already replaced it we simply skip — the swap preserves any width/height we
 * managed to set via `...imageNode.attrs`.
 */
async function applyImageDimensions(editor: any, file: File, src: string) {
  const dims = await readImageDimensions(file);
  if (!dims) return;

  const imagePos = findImagePosBySrc(editor, src);
  if (imagePos === null) return;

  const imageNode = editor.state.doc.nodeAt(imagePos);
  if (!imageNode || imageNode.attrs.width) return;

  const tr = editor.state.tr.setNodeMarkup(imagePos, undefined, {
    ...imageNode.attrs,
    width: dims.width,
    height: dims.height,
  });
  editor.view.dispatch(tr);
}

function moveSelectionToParagraphAfterImage(editor: any, src: string) {
  const imagePos = findImagePosBySrc(editor, src);
  if (imagePos === null) return;

  const imageNode = editor.state.doc.nodeAt(imagePos);
  if (!imageNode) return;

  const afterImagePos = imagePos + imageNode.nodeSize;
  const $afterImage = editor.state.doc.resolve(afterImagePos);
  if ($afterImage.nodeAfter?.type.name !== "paragraph") return;

  const paragraphStart = afterImagePos + 1;
  const tr = editor.state.tr
    .setSelection(TextSelection.create(editor.state.doc, paragraphStart))
    .scrollIntoView();
  editor.view.dispatch(tr);
}

/**
 * Shared upload flow: insert blob preview → upload → replace with real URL.
 * Used by both paste/drop (at cursor) and button upload (at end of doc).
 */
export async function uploadAndInsertFile(

  editor: any,
  file: File,
  handler: (file: File) => Promise<UploadResult | null>,
  pos?: number,
) {
  const isImage = file.type.startsWith("image/");

  if (isImage) {
    const blobUrl = URL.createObjectURL(file);
    const imgAttrs = { src: blobUrl, alt: file.name, uploading: true };
    if (pos !== undefined) {
      editor.chain().focus().insertContentAt(pos, { type: "image", attrs: imgAttrs }).run();
    } else {
      editor.chain().focus().setImage(imgAttrs).run();
      moveSelectionToParagraphAfterImage(editor, blobUrl);
    }

    // Reserve the image box ASAP so the async decode doesn't shift layout.
    // Fire-and-forget: must not delay the handler() call below, which the
    // synchronous-insert contract (instant preview) depends on.
    void applyImageDimensions(editor, file, blobUrl);

    try {
      const result = await handler(file);
      // The upload outlives the mount (coordinator-owned, MUL-5181): by the
      // time it settles this editor may be destroyed. Dispatching against a
      // destroyed EditorView throws, and the catch would dispatch again —
      // the write-back path owns delivery for dead editors, not this swap.
      if (editor.isDestroyed) return;
      if (result) {
        const imagePos = findImagePosBySrc(editor, blobUrl);
        const imageNode = imagePos === null ? null : editor.state.doc.nodeAt(imagePos);
        if (imagePos !== null && imageNode) {
          const tr = editor.state.tr.setNodeMarkup(imagePos, undefined, {
            ...imageNode.attrs,
            // Persist the stable per-attachment URL into markdown so
            // the comment doesn't capture a short-lived signed URL
            // (MUL-3130). Falls back to `link` for the no-workspace
            // avatar branch where there's no attachment-row id; that
            // path is unreachable from comment/issue editors but the
            // fallback keeps the contract consistent for any caller
            // that drops in without an issue context.
            src: result.markdownLink || result.link,
            alt: result.filename,
            uploading: false,
          });
          editor.view.dispatch(tr);
        }
      } else {
        removeImageBySrc(editor, blobUrl);
      }
    } catch {
      if (!editor.isDestroyed) removeImageBySrc(editor, blobUrl);
    } finally {
      URL.revokeObjectURL(blobUrl);
    }
  } else {
    // Non-image: insert skeleton fileCard → upload → finalize with real URL
    const uploadId = createSafeId();
    const cardAttrs = { filename: file.name, href: "", fileSize: file.size, uploading: true, uploadId };
    const insertContent = { type: "fileCard", attrs: cardAttrs };
    if (pos !== undefined) {
      editor.chain().focus().insertContentAt(pos, insertContent).run();
    } else {
      editor.chain().focus().insertContent(insertContent).run();
    }

    try {
      const result = await handler(file);
      // See the image branch: a settle after this editor's destroy must not
      // dispatch against the dead EditorView.
      if (editor.isDestroyed) return;
      if (result) {
        finalizeFileCard(editor, uploadId, result.markdownLink || result.link);
      } else {
        removeUploadingFileCard(editor, uploadId);
      }
    } catch {
      if (!editor.isDestroyed) removeUploadingFileCard(editor, uploadId);
    }
  }
}

/** Deduplicate files from the same paste/drop event.
 *  macOS/Chrome can put the same file in the FileList twice. */
function dedupFiles(files: FileList): File[] {
  const seen = new Set<string>();
  return Array.from(files).filter((file) => {
    const key = `${file.name}\0${file.size}\0${file.type}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

/** Filename given to the .txt synthesised from an over-threshold paste. */
export const PASTED_TEXT_FILENAME = "pasted-text.txt";

/**
 * Source text of every file synthesised by the paste-as-file path, keyed by
 * the File instance that carries it downstream.
 *
 * Unlike a dropped file, this one has no copy anywhere else: the text was
 * never written into the document and its source may be a tab the user has
 * already closed. So whoever owns the draft must be able to put it back when
 * the upload fails. A WeakMap rather than a property on the File keeps the
 * File exactly as the upload layer expects it, and lets the entry die with the
 * upload. Read it with {@link pastedTextSource}; a File that came from disk
 * returns undefined and needs no recovery.
 */
const pastedTextSources = new WeakMap<File, string>();

/** Record the text a synthesised paste file was built from. */
export function markPastedTextFile(file: File, text: string): File {
  pastedTextSources.set(file, text);
  return file;
}

/** The text an over-threshold paste was made from, or undefined for real files. */
export function pastedTextSource(file: File): string | undefined {
  return pastedTextSources.get(file);
}

export function createFileUploadExtension(
  onUploadFileRef: React.RefObject<((file: File) => Promise<UploadResult | null>) | undefined>,
  /**
   * Character count above which a plain-text paste is uploaded as a .txt
   * attachment instead of being inserted into the document. A ref because the
   * extension array is built once at mount while the prop that feeds it can
   * change. Undefined / 0 keeps every paste as text — the default, so an
   * editor that never opts in behaves exactly as before.
   */
  pasteAsFileThresholdRef?: React.RefObject<number | undefined>,
) {
  return Extension.create({
    name: "fileUpload",
    addProseMirrorPlugins() {
      const { editor } = this;

      const handleFiles = async (files: File[]) => {
        const handler = onUploadFileRef.current;
        if (!handler) return false;
        for (const file of files) {
          await uploadAndInsertFile(editor, file, handler);
        }
        return true;
      };

      return [
        new Plugin({
          key: new PluginKey("fileUpload"),
          props: {
            handlePaste(_view, event) {
              const files = event.clipboardData?.files;
              if (!files?.length) {
                // No file on the clipboard: this may still be a paste large
                // enough that the host wants it as an attachment rather than
                // thousands of characters of body text (turn-based composers
                // only — document editors never pass a threshold).
                const threshold = pasteAsFileThresholdRef?.current;
                if (!threshold || threshold <= 0) return false;
                const text = event.clipboardData?.getData("text/plain") ?? "";
                if (text.length <= threshold) return false;
                if (!onUploadFileRef.current) return false;
                // A paste INTO a code block is the one long paste that is
                // deliberately inline — the user opened a fence to show the
                // thing. Converting it to an attachment would take away what
                // they just asked for.
                if (editor.isActive("codeBlock")) return false;
                const file = new File([text], PASTED_TEXT_FILENAME, { type: "text/plain" });
                handleFiles([markPastedTextFile(file, text)]);
                return true;
              }
              if (!onUploadFileRef.current) return false;
              handleFiles(dedupFiles(files));
              return true;
            },
            handleDrop(view, event) {
              const dragEvent = event as DragEvent;
              const files = dragEvent.dataTransfer?.files;
              if (!files?.length) return false;
              const handler = onUploadFileRef.current;
              if (!handler) return false;
              // Resolve drop position from mouse coordinates.
              // Only the first file uses the drop position; subsequent files
              // append to the end to avoid stale position issues.
              const dropPos = view.posAtCoords({ left: dragEvent.clientX, top: dragEvent.clientY });
              const unique = dedupFiles(files);
              for (let i = 0; i < unique.length; i++) {
                const insertPos = i === 0 ? dropPos?.pos : undefined;
                uploadAndInsertFile(editor, unique[i]!, handler, insertPos);
              }
              return true;
            },
          },
        }),
      ];
    },
  });
}
