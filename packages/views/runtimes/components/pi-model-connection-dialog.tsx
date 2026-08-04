"use client";

import { useEffect, useMemo, useState } from "react";
import { Loader2, LockKeyhole } from "lucide-react";
import { toast } from "sonner";
import {
  PI_API_TYPES,
  type PiApiType,
  type PiRuntimeConfig,
  parsePiRuntimeConfig,
  validatePiRuntimeConfig,
} from "@multica/core/agents";
import { useUpdateRuntimeModelConnection } from "@multica/core/runtimes/mutations";
import type { AgentRuntime } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { useT } from "../../i18n";

type ProviderPreset = "deepseek" | "custom";

type Draft = {
  preset: ProviderPreset;
  provider: string;
  api: PiApiType;
  baseUrl: string;
  model: string;
  apiKey: string;
};

const DEEPSEEK_MODELS = ["deepseek-v4-flash", "deepseek-v4-pro"] as const;

function configToDraft(runtime: AgentRuntime): Draft {
  const config = parsePiRuntimeConfig(runtime.default_model_config);
  const isDeepSeek =
    config.provider === "deepseek" &&
    config.baseUrl?.replace(/\/+$/, "") === "https://api.deepseek.com";
  const isKnownDeepSeekModel = DEEPSEEK_MODELS.includes(
    config.model as (typeof DEEPSEEK_MODELS)[number],
  );

  if (
    (isDeepSeek && isKnownDeepSeekModel) ||
    Object.keys(config).length === 0
  ) {
    return {
      preset: "deepseek",
      provider: "deepseek",
      api: "openai-completions",
      baseUrl: "https://api.deepseek.com",
      model: config.model ?? DEEPSEEK_MODELS[0],
      apiKey: "",
    };
  }

  return {
    preset: "custom",
    provider: config.provider ?? "",
    api: config.api ?? "openai-responses",
    baseUrl: config.baseUrl ?? "",
    model: config.model ?? "",
    apiKey: "",
  };
}

function draftToConfig(draft: Draft): PiRuntimeConfig {
  return {
    provider: draft.provider.trim(),
    api: draft.api,
    baseUrl: draft.baseUrl.trim(),
    model: draft.model.trim(),
  };
}

export function PiModelConnectionDialog({
  runtime,
  wsId,
  open,
  onOpenChange,
}: {
  runtime: AgentRuntime;
  wsId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useT("runtimes");
  const updateConnection = useUpdateRuntimeModelConnection(wsId);
  const [draft, setDraft] = useState(() => configToDraft(runtime));

  useEffect(() => {
    if (open) setDraft(configToDraft(runtime));
  }, [open, runtime]);

  const config = useMemo(() => draftToConfig(draft), [draft]);
  const validationError = validatePiRuntimeConfig(config);
  const needsKey = runtime.has_default_model_api_key !== true;
  const canSave =
    !validationError &&
    (!needsKey || draft.apiKey.trim().length > 0) &&
    !updateConnection.isPending;

  const selectPreset = (preset: ProviderPreset) => {
    if (preset === "deepseek") {
      setDraft((current) => ({
        ...current,
        preset,
        provider: "deepseek",
        api: "openai-completions",
        baseUrl: "https://api.deepseek.com",
        model: DEEPSEEK_MODELS.includes(
          current.model as (typeof DEEPSEEK_MODELS)[number],
        )
          ? current.model
          : DEEPSEEK_MODELS[0],
      }));
      return;
    }
    setDraft((current) => ({ ...current, preset }));
  };

  const save = () => {
    if (!canSave) return;
    const apiKey = draft.apiKey.trim();
    updateConnection.mutate(
      {
        runtimeId: runtime.id,
        connection: {
          provider: config.provider!,
          api: config.api!,
          base_url: config.baseUrl!,
          model: config.model!,
          ...(apiKey ? { api_key: apiKey } : {}),
        },
      },
      {
        onSuccess: () => {
          toast.success(t(($) => $.detail.model_connection.toast_saved));
          onOpenChange(false);
        },
        onError: (error) => {
          toast.error(
            error instanceof Error && error.message
              ? error.message
              : t(($) => $.detail.model_connection.toast_failed),
          );
        },
      },
    );
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!updateConnection.isPending) onOpenChange(next);
      }}
    >
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {t(($) => $.detail.model_connection.dialog_title)}
          </DialogTitle>
          <DialogDescription>
            {t(($) => $.detail.model_connection.dialog_description)}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="pi-connection-preset">
              {t(($) => $.detail.model_connection.provider_label)}
            </Label>
            <select
              id="pi-connection-preset"
              value={draft.preset}
              onChange={(event) =>
                selectPreset(event.target.value as ProviderPreset)
              }
              className="h-9 w-full rounded-md border border-input bg-background px-3 text-body"
            >
              <option value="deepseek">
                {t(($) => $.detail.model_connection.deepseek_provider)}
              </option>
              <option value="custom">
                {t(($) => $.detail.model_connection.custom_provider)}
              </option>
            </select>
          </div>

          {draft.preset === "deepseek" ? (
            <div className="space-y-1.5">
              <Label htmlFor="pi-connection-model">
                {t(($) => $.detail.model_connection.model_label)}
              </Label>
              <select
                id="pi-connection-model"
                value={draft.model}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    model: event.target.value,
                  }))
                }
                className="h-9 w-full rounded-md border border-input bg-background px-3 font-mono text-caption"
              >
                {DEEPSEEK_MODELS.map((model) => (
                  <option key={model} value={model}>
                    {model}
                  </option>
                ))}
              </select>
            </div>
          ) : (
            <div className="grid gap-3 sm:grid-cols-2">
              <ConnectionField
                id="pi-connection-provider"
                label={t(($) => $.detail.model_connection.provider_id_label)}
                value={draft.provider}
                placeholder="openai"
                onChange={(provider) =>
                  setDraft((current) => ({ ...current, provider }))
                }
              />
              <div className="space-y-1.5">
                <Label htmlFor="pi-connection-api">
                  {t(($) => $.detail.model_connection.api_label)}
                </Label>
                <select
                  id="pi-connection-api"
                  value={draft.api}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      api: event.target.value as PiApiType,
                    }))
                  }
                  className="h-9 w-full rounded-md border border-input bg-background px-3 font-mono text-caption"
                >
                  {PI_API_TYPES.map((api) => (
                    <option key={api} value={api}>
                      {api}
                    </option>
                  ))}
                </select>
              </div>
              <ConnectionField
                id="pi-connection-base-url"
                label={t(($) => $.detail.model_connection.base_url_label)}
                value={draft.baseUrl}
                placeholder="https://api.example.com/v1"
                invalid={validationError === "base_url"}
                onChange={(baseUrl) =>
                  setDraft((current) => ({ ...current, baseUrl }))
                }
                className="sm:col-span-2"
              />
              <ConnectionField
                id="pi-connection-custom-model"
                label={t(($) => $.detail.model_connection.model_label)}
                value={draft.model}
                placeholder="model-id"
                onChange={(model) =>
                  setDraft((current) => ({ ...current, model }))
                }
                className="sm:col-span-2"
              />
            </div>
          )}

          <div className="space-y-1.5 rounded-md border p-3">
            <Label
              htmlFor="pi-connection-api-key"
              className="flex items-center gap-2"
            >
              <LockKeyhole className="h-3.5 w-3.5 text-muted-foreground" />
              {t(($) => $.detail.model_connection.api_key_label)}
            </Label>
            <Input
              id="pi-connection-api-key"
              type="password"
              autoComplete="new-password"
              value={draft.apiKey}
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  apiKey: event.target.value,
                }))
              }
              placeholder={
                needsKey
                  ? t(($) => $.detail.model_connection.api_key_placeholder)
                  : t(($) => $.detail.model_connection.api_key_saved)
              }
            />
            <p className="text-caption text-muted-foreground">
              {needsKey
                ? t(($) => $.detail.model_connection.api_key_hint)
                : t(($) => $.detail.model_connection.api_key_preserve_hint)}
            </p>
          </div>

          {validationError && (
            <p className="text-caption text-destructive">
              {validationError === "provider"
                ? t(($) => $.detail.model_connection.provider_error)
                : validationError === "base_url"
                  ? t(($) => $.detail.model_connection.base_url_error)
                  : t(($) => $.detail.model_connection.incomplete_error)}
            </p>
          )}
        </div>

        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={updateConnection.isPending}
          >
            {t(($) => $.detail.model_connection.cancel)}
          </Button>
          <Button onClick={save} disabled={!canSave}>
            {updateConnection.isPending && (
              <Loader2 className="h-4 w-4 animate-spin" />
            )}
            {updateConnection.isPending
              ? t(($) => $.detail.model_connection.saving)
              : t(($) => $.detail.model_connection.save)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ConnectionField({
  id,
  label,
  value,
  placeholder,
  invalid,
  onChange,
  className = "",
}: {
  id: string;
  label: string;
  value: string;
  placeholder: string;
  invalid?: boolean;
  onChange: (value: string) => void;
  className?: string;
}) {
  return (
    <div className={`space-y-1.5 ${className}`}>
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={placeholder}
        aria-invalid={invalid || undefined}
        className="font-mono text-caption"
      />
    </div>
  );
}
