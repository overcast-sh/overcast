/**
 * CreateFunctionWizard — multi-step dialog for creating a Lambda function.
 *
 * Step 1: Package type (Zip / Container Image) + configuration
 *   - Zip:   name, runtime, handler, role
 *   - Image: name, image URI, role
 *
 * Step 2: Code source
 *   - Zip:   inline editor | .zip upload | S3 object  (+ optional SourceKMSKeyArn)
 *   - Image: optional SourceKMSKeyArn only
 *
 * After creation the wizard pushes the source/zip/S3/image code to the
 * function (if needed) and navigates to the function detail page.
 */
import { useState, useCallback, useMemo } from "react"
import { useNavigate } from "@tanstack/react-router"
import { useForm } from "@tanstack/react-form"
import { z } from "zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  Check,
  Code2,
  ChevronLeft,
  ChevronRight,
  Box,
  Archive,
  Cloud,
  Plus,
  Trash2,
  Settings,
  Variable,
  ClipboardPaste,
} from "lucide-react"
import Editor from "@monaco-editor/react"
import { Button } from "@/components/ui/button"
import { MONACO_BASE_OPTIONS, monacoTheme } from "@/components/ui/monaco-setup"
import { Input } from "@/components/ui/input"
import { Select } from "@/components/ui/select"
import { FormField, FormRow, fieldError } from "@/components/ui/form"
import { Combobox } from "@/components/ui/combobox"
import { Spinner } from "@/components/ui/primitives"
import { StepDot, WizardOptionCard, ZipDropzone, ZipS3SourceFields } from "./wizard-primitives"
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { useToast } from "@/components/ui/toast"
import {
  lambdaKeys,
  createFunctionMutationOptions,
  putSourceMutationOptions,
  lambdaRuntimesQueryOptions,
} from "@/features/lambda/data"
import {
  APPLICATION_LOG_LEVELS,
  buildLoggingConfig,
  DEFAULT_APPLICATION_LOG_LEVEL,
  DEFAULT_SYSTEM_LOG_LEVEL,
  LOG_FORMATS,
  SYSTEM_LOG_LEVELS,
  type LoggingConfigForm,
} from "@/features/lambda/logging-config"
import { logsGroupsQueryOptions } from "@/features/cloudwatch/logs/data"
import { lambda } from "@/services/api"
import type { Runtime } from "@aws-sdk/client-lambda"
import { buildRuntimeItems, type RuntimeItem } from "@/features/lambda/runtime-items"
import { canReadClipboardText, readClipboardText } from "@/lib/clipboard"
import { cn } from "@/lib/utils"
import { formatQuantity } from "@/lib/format"

// ─── Runtime data ────────────────────────────────────────────────────────────

/** Fallback defaults when runtimes haven't loaded yet. */
const DEFAULT_RUNTIME = "nodejs22.x"
const DEFAULT_HANDLER = "index.handler"

function runtimeLanguage(runtime: string): string {
  if (runtime.startsWith("nodejs")) return "javascript"
  if (runtime.startsWith("python")) return "python"
  if (runtime.startsWith("java")) return "java"
  if (runtime.startsWith("dotnet")) return "csharp"
  if (runtime.startsWith("ruby")) return "ruby"
  return "plaintext"
}

function defaultSource(runtime: string, handler: string): { source: string; filename: string } {
  const parts = handler.split(".")
  if (runtime.startsWith("nodejs")) {
    const file = (parts[0] || "index") + ".mjs"
    const fn = parts[1] || "handler"
    return {
      filename: file,
      source: `export const ${fn} = async (event) => {\n  console.log("Event:", JSON.stringify(event, null, 2));\n  return {\n    statusCode: 200,\n    body: JSON.stringify({ message: "Hello from Lambda!" }),\n  };\n};\n`,
    }
  }
  if (runtime.startsWith("python")) {
    const file = (parts[0] || "lambda_function") + ".py"
    const fn = parts[1] || "handler"
    return {
      filename: file,
      source: `import json\n\ndef ${fn}(event, context):\n    print("Event:", json.dumps(event))\n    return {\n        "statusCode": 200,\n        "body": json.dumps({"message": "Hello from Lambda!"}),\n    }\n`,
    }
  }
  if (runtime.startsWith("java")) {
    return {
      filename: "Handler.java",
      source: `package example;\n\nimport com.amazonaws.services.lambda.runtime.Context;\nimport com.amazonaws.services.lambda.runtime.RequestHandler;\n\npublic class Handler implements RequestHandler<Object, String> {\n    @Override\n    public String handleRequest(Object event, Context context) {\n        context.getLogger().log("Event: " + event);\n        return "Hello from Lambda!";\n    }\n}\n`,
    }
  }
  if (runtime.startsWith("ruby")) {
    const file = (parts[0] || "lambda_function") + ".rb"
    const fn = parts[1] || "lambda_handler"
    return {
      filename: file,
      source: `require 'json'\n\ndef ${fn}(event:, context:)\n  puts "Event: #{event.to_json}"\n  { statusCode: 200, body: JSON.generate({ message: 'Hello from Lambda!' }) }\nend\n`,
    }
  }
  if (runtime.startsWith("dotnet")) {
    return {
      filename: "Function.cs",
      source: `using Amazon.Lambda.Core;\n\n[assembly: LambdaSerializer(typeof(Amazon.Lambda.Serialization.SystemTextJson.DefaultLambdaJsonSerializer))]\n\nnamespace LambdaFunction;\n\npublic class Function\n{\n    public string FunctionHandler(object input, ILambdaContext context)\n    {\n        context.Logger.LogInformation($"Event: {input}");\n        return "Hello from Lambda!";\n    }\n}\n`,
    }
  }
  return { filename: "handler.sh", source: "#!/bin/sh\necho 'Hello from Lambda!'\n" }
}

// ─── Types ────────────────────────────────────────────────────────────────────

type PkgType = "Zip" | "Image"
type ZipCodeSource = "inline" | "zip" | "s3"

/**
 * Parse a .env-formatted string into key-value pairs.
 * Supports: KEY=value, KEY="value", KEY='value', export KEY=value,
 * blank lines, and # comments.
 */
function parseEnvString(text: string): { key: string; value: string }[] {
  const entries: { key: string; value: string }[] = []
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim()
    if (!line || line.startsWith("#")) continue
    // Strip optional "export " prefix
    const stripped = line.startsWith("export ") ? line.slice(7) : line
    const eqIdx = stripped.indexOf("=")
    if (eqIdx < 1) continue
    const key = stripped.slice(0, eqIdx).trim()
    let value = stripped.slice(eqIdx + 1).trim()
    // Remove surrounding quotes
    if (
      (value.startsWith('"') && value.endsWith('"')) ||
      (value.startsWith("'") && value.endsWith("'"))
    ) {
      value = value.slice(1, -1)
    }
    // Remove inline comment (only for unquoted values)
    if (
      !stripped
        .slice(eqIdx + 1)
        .trim()
        .startsWith('"') &&
      !stripped
        .slice(eqIdx + 1)
        .trim()
        .startsWith("'")
    ) {
      const commentIdx = value.indexOf(" #")
      if (commentIdx >= 0) value = value.slice(0, commentIdx).trimEnd()
    }
    if (/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) {
      entries.push({ key, value })
    }
  }
  return entries
}

// ─── Component ───────────────────────────────────────────────────────────────

interface CreateFunctionWizardProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function CreateFunctionWizard({ open, onOpenChange }: CreateFunctionWizardProps) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { toast } = useToast()

  const [step, setStep] = useState(1)

  // Fetch runtimes dynamically from the backend (cached for the session)
  const { data: runtimesData, isLoading: runtimesLoading } = useQuery(lambdaRuntimesQueryOptions())
  const runtimeItems = useMemo(
    () => (runtimesData ? buildRuntimeItems(runtimesData) : []),
    [runtimesData],
  )

  // Fetch existing log groups for the combobox
  const { data: existingLogGroups } = useQuery({
    ...logsGroupsQueryOptions(),
    staleTime: 30_000,
  })
  const logGroupItems = useMemo(
    () => (existingLogGroups ?? []).map((g) => ({ value: g.logGroupName ?? "" })),
    [existingLogGroups],
  )

  const [pkgType, setPkgType] = useState<PkgType>("Zip")
  const [codeSource, setCodeSource] = useState<ZipCodeSource>("inline")

  // Inline editor
  const [editorValue, setEditorValue] = useState("")
  // Zip upload
  const [zipFile, setZipFile] = useState<File | null>(null)
  // S3
  const [s3Bucket, setS3Bucket] = useState("")
  const [s3Key, setS3Key] = useState("")
  const [s3ObjectVersion, setS3ObjectVersion] = useState("")
  // Shared optional
  const [sourceKMSKeyArn, setSourceKMSKeyArn] = useState("")
  const [showKMS, setShowKMS] = useState(false)
  // Logging — the log group itself is a validated form field; the format and
  // its two levels are picked from fixed lists, so they need no validation.
  const [logFormat, setLogFormat] = useState<LoggingConfigForm["logFormat"]>("Text")
  const [applicationLogLevel, setApplicationLogLevel] = useState<
    LoggingConfigForm["applicationLogLevel"]
  >(DEFAULT_APPLICATION_LOG_LEVEL)
  const [systemLogLevel, setSystemLogLevel] =
    useState<LoggingConfigForm["systemLogLevel"]>(DEFAULT_SYSTEM_LOG_LEVEL)
  // Environment variables
  const [envVars, setEnvVars] = useState<{ key: string; value: string }[]>([])
  const [showEnvVars, setShowEnvVars] = useState(false)
  const canPasteEnv = canReadClipboardText()
  // General configuration
  const [showAdvanced, setShowAdvanced] = useState(false)

  const [isCreating, setIsCreating] = useState(false)

  const resetState = () => {
    setStep(1)
    setPkgType("Zip")
    setCodeSource("inline")
    setEditorValue("")
    setZipFile(null)
    setS3Bucket("")
    setS3Key("")
    setS3ObjectVersion("")
    setSourceKMSKeyArn("")
    setShowKMS(false)
    setLogFormat("Text")
    setApplicationLogLevel(DEFAULT_APPLICATION_LOG_LEVEL)
    setSystemLogLevel(DEFAULT_SYSTEM_LOG_LEVEL)
    setEnvVars([])
    setShowEnvVars(false)
    setShowAdvanced(false)
    setIsCreating(false)
    form.reset()
  }

  const handleOpenChange = (v: boolean) => {
    if (!v) resetState()
    onOpenChange(v)
  }

  const form = useForm({
    validators: {
      onChange: z.object({
        functionName: z
          .string()
          .min(1, "Required")
          .max(64, "Max 64 chars")
          .regex(/^[A-Za-z0-9_-]+$/, "Letters, digits, - and _ only"),
        runtime: z.string(),
        handler: z.string(),
        imageUri: z.string(),
        role: z.string().min(1, "Required"),
        logGroup: z.string(),
        description: z.string().max(256, "Max 256 chars"),
        memorySize: z.number().min(128, "Min 128 MB").max(10240, "Max 10240 MB"),
        timeout: z.number().min(1, "Min 1s").max(900, "Max 900s"),
      }),
    },
    defaultValues: {
      functionName: "",
      runtime: DEFAULT_RUNTIME,
      handler: DEFAULT_HANDLER,
      imageUri: "",
      role: "arn:aws:iam::000000000000:role/lambda-role",
      logGroup: "",
      description: "",
      memorySize: 128,
      timeout: 3,
    },
    onSubmit: () => {
      // Handled manually
    },
  })

  const { mutateAsync: createMutateAsync } = useMutation(createFunctionMutationOptions())
  const { mutateAsync: putSourceMutateAsync } = useMutation(putSourceMutationOptions())

  const handleAdvance = useCallback(() => {
    const v = form.state.values
    if (!v.functionName.trim() || !v.role.trim()) return
    if (pkgType === "Zip") {
      if (!v.runtime || !v.handler.trim()) return
      if (codeSource === "inline") {
        const def = defaultSource(v.runtime, v.handler)
        setEditorValue(def.source)
      }
    } else {
      if (!v.imageUri.trim()) return
    }
    setStep(2)
  }, [form, pkgType, codeSource])

  const handleCreate = useCallback(async () => {
    const v = form.state.values
    setIsCreating(true)
    try {
      // Build environment map
      const envMap: Record<string, string> = {}
      for (const { key, value } of envVars) {
        const k = key.trim()
        if (k) envMap[k] = value
      }

      // Create the function
      await createMutateAsync({
        FunctionName: v.functionName,
        Runtime: pkgType === "Zip" ? (v.runtime as Runtime) : undefined,
        Handler: pkgType === "Zip" ? v.handler : undefined,
        Role: v.role,
        PackageType: pkgType,
        Code: pkgType === "Image" ? { ImageUri: v.imageUri } : { ZipFile: new Uint8Array(0) },
        Description: v.description || undefined,
        MemorySize: v.memorySize !== 128 ? v.memorySize : undefined,
        Timeout: v.timeout !== 3 ? v.timeout : undefined,
        Environment: Object.keys(envMap).length > 0 ? { Variables: envMap } : undefined,
        LoggingConfig: buildLoggingConfig({
          logGroup: v.logGroup,
          logFormat,
          applicationLogLevel,
          systemLogLevel,
        }),
      })

      // Push code
      const kms = sourceKMSKeyArn.trim() || undefined
      if (pkgType === "Zip") {
        if (codeSource === "inline") {
          const def = defaultSource(v.runtime, v.handler)
          await putSourceMutateAsync({
            name: v.functionName,
            source: editorValue,
            filename: def.filename,
          })
        } else if (codeSource === "zip" && zipFile) {
          await lambda.putCode(v.functionName, {
            type: "zip",
            file: zipFile,
            sourceKMSKeyArn: kms,
          })
        } else if (codeSource === "s3") {
          await lambda.putCode(v.functionName, {
            type: "s3",
            s3Bucket,
            s3Key,
            s3ObjectVersion: s3ObjectVersion.trim() || undefined,
            sourceKMSKeyArn: kms,
          })
        }
      } else if (kms) {
        // Image URI was already passed in CreateFunction; only call UpdateFunctionCode when
        // a SourceKMSKeyArn is specified (to set encryption on the image).
        await lambda.putCode(v.functionName, {
          type: "image",
          imageUri: v.imageUri,
          sourceKMSKeyArn: kms,
        })
      }

      void qc.invalidateQueries({ queryKey: lambdaKeys.functions() })
      toast({ title: "Function created", description: v.functionName, variant: "success" })
      handleOpenChange(false)
      void navigate({ to: "/lambda/$name", params: { name: v.functionName } })
    } catch (err) {
      toast({ title: "Create failed", description: (err as Error).message, variant: "danger" })
    } finally {
      setIsCreating(false)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    form,
    pkgType,
    codeSource,
    editorValue,
    zipFile,
    s3Bucket,
    s3Key,
    s3ObjectVersion,
    sourceKMSKeyArn,
    logFormat,
    applicationLogLevel,
    systemLogLevel,
    envVars,
    createMutateAsync,
    putSourceMutateAsync,
    qc,
    toast,
    navigate,
  ])

  const isDark =
    document.documentElement.getAttribute("data-theme") === "dark" ||
    (document.documentElement.getAttribute("data-theme") === null &&
      window.matchMedia("(prefers-color-scheme: dark)").matches)

  const runtime = form.getFieldValue("runtime")
  const handler = form.getFieldValue("handler")
  const def = defaultSource(runtime, handler)

  const canCreate =
    pkgType === "Image" ||
    codeSource === "inline" ||
    (codeSource === "zip" && !!zipFile) ||
    (codeSource === "s3" && !!s3Bucket.trim() && !!s3Key.trim())

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            Create function
            <span className="ml-2 text-xs font-normal text-fg-muted">Step {step} of 2</span>
          </DialogTitle>
        </DialogHeader>

        <DialogBody className="pr-1">
          {/* ─── Step indicator ──────────────────────────────────────────────── */}
          <div className="flex items-center gap-2 pb-2 text-xs text-fg-muted">
            <StepDot label="Configuration" active={step === 1} done={step > 1} />
            <div className="h-px w-6 bg-border" />
            <StepDot
              label={pkgType === "Image" ? "Encryption" : "Code"}
              active={step === 2}
              done={false}
            />
          </div>

          {/* ─── Step 1: Configuration ───────────────────────────────────────── */}
          {step === 1 && (
            <div className="flex flex-col gap-3">
              {/* Package type */}
              <FormRow>
                <FormField label="Package type">
                  <div className="flex gap-2">
                    <WizardOptionCard
                      active={pkgType === "Zip"}
                      icon={<Archive className="h-4 w-4" />}
                      label="Zip-based"
                      description="Deploy code as a .zip archive"
                      onClick={() => {
                        setPkgType("Zip")
                        setCodeSource("inline")
                      }}
                    />
                    <WizardOptionCard
                      active={pkgType === "Image"}
                      icon={<Box className="h-4 w-4" />}
                      label="Container image"
                      description="Deploy from an OCI image URI"
                      onClick={() => setPkgType("Image")}
                    />
                  </div>
                </FormField>
              </FormRow>

              {/* Function name */}
              <form.Field name="functionName">
                {(field) => (
                  <FormRow>
                    <FormField label="Function name" error={fieldError(field.state.meta.errors)}>
                      <Input
                        value={field.state.value}
                        onChange={(e) => field.handleChange(e.target.value)}
                        placeholder="my-function"
                        autoFocus
                      />
                    </FormField>
                  </FormRow>
                )}
              </form.Field>

              {/* Zip-specific fields */}
              {pkgType === "Zip" && (
                <>
                  <form.Field name="runtime">
                    {(field) => (
                      <FormRow>
                        <FormField label="Runtime" error={fieldError(field.state.meta.errors)}>
                          <Combobox<RuntimeItem>
                            isLoading={runtimesLoading}
                            value={field.state.value}
                            onChange={(v) => {
                              field.handleChange(v)
                              const rt = runtimeItems.find((r) => r.value === v)
                              if (rt) form.setFieldValue("handler", rt.defaultHandler)
                            }}
                            items={runtimeItems}
                            filterFn={(item, q) =>
                              item.value.toLowerCase().includes(q.toLowerCase()) ||
                              item.group.toLowerCase().includes(q.toLowerCase())
                            }
                            getItemValue={(item) => item.value}
                            renderItem={(item, { selected, active }) => (
                              <div className="flex items-center justify-between gap-2">
                                <span className={cn(active && "text-white")}>
                                  {item.value}
                                  {item.deprecated && (
                                    <span
                                      className={cn(
                                        "ml-2 text-xs",
                                        active ? "text-white" : "text-fg-muted",
                                      )}
                                      title="AWS has ended support for this runtime. It still deploys until AWS blocks new functions."
                                    >
                                      deprecated
                                    </span>
                                  )}
                                </span>
                                {selected && (
                                  <Check
                                    className={cn(
                                      "h-3.5 w-3.5 shrink-0",
                                      active ? "text-white" : "text-accent",
                                    )}
                                  />
                                )}
                              </div>
                            )}
                            renderSeparator={(item, prev) =>
                              item.group !== prev?.group ? (
                                <div className="px-3 pt-2 pb-0.5 text-xs font-semibold text-fg-muted select-none">
                                  {item.group}
                                </div>
                              ) : null
                            }
                            placeholder="Select a runtime…"
                          />
                        </FormField>
                      </FormRow>
                    )}
                  </form.Field>
                  <form.Field name="handler">
                    {(field) => (
                      <FormRow>
                        <FormField label="Handler" error={fieldError(field.state.meta.errors)}>
                          <Input
                            value={field.state.value}
                            onChange={(e) => field.handleChange(e.target.value)}
                            placeholder="index.handler"
                          />
                        </FormField>
                      </FormRow>
                    )}
                  </form.Field>
                </>
              )}

              {/* Image-specific field */}
              {pkgType === "Image" && (
                <form.Field name="imageUri">
                  {(field) => (
                    <FormRow>
                      <FormField label="Image URI" error={fieldError(field.state.meta.errors)}>
                        <Input
                          value={field.state.value}
                          onChange={(e) => field.handleChange(e.target.value)}
                          placeholder="123456789012.dkr.ecr.us-east-1.amazonaws.com/my-fn:latest"
                        />
                      </FormField>
                    </FormRow>
                  )}
                </form.Field>
              )}

              {/* Execution role */}
              <form.Field name="role">
                {(field) => (
                  <FormRow>
                    <FormField
                      label="Execution role ARN"
                      error={fieldError(field.state.meta.errors)}
                    >
                      <Input
                        value={field.state.value}
                        onChange={(e) => field.handleChange(e.target.value)}
                        placeholder="arn:aws:iam::000000000000:role/lambda-role"
                      />
                    </FormField>
                  </FormRow>
                )}
              </form.Field>

              {/* Description */}
              <form.Field name="description">
                {(field) => (
                  <FormRow>
                    <FormField
                      label="Description"
                      hint="Optional"
                      error={fieldError(field.state.meta.errors)}
                    >
                      <Input
                        value={field.state.value}
                        onChange={(e) => field.handleChange(e.target.value)}
                        placeholder="A brief description of your function"
                      />
                    </FormField>
                  </FormRow>
                )}
              </form.Field>

              {/* Log group */}
              <form.Field name="logGroup">
                {(field) => (
                  <FormRow>
                    <FormField
                      label="Log group"
                      hint={`Optional · defaults to /aws/lambda/{name}`}
                      error={fieldError(field.state.meta.errors)}
                    >
                      <Combobox<{ value: string }>
                        value={field.state.value}
                        onChange={field.handleChange}
                        items={logGroupItems}
                        allowCustom
                        filterFn={(item, q) => item.value.toLowerCase().includes(q.toLowerCase())}
                        getItemValue={(item) => item.value}
                        renderItem={(item, { selected, active }) => (
                          <div className="flex items-center justify-between">
                            <span className={cn("font-mono text-xs", active && "text-white")}>
                              {item.value}
                            </span>
                            {selected && (
                              <Check
                                className={cn(
                                  "h-3.5 w-3.5 shrink-0",
                                  active ? "text-white" : "text-accent",
                                )}
                              />
                            )}
                          </div>
                        )}
                        placeholder="/aws/lambda/my-function"
                      />
                    </FormField>
                  </FormRow>
                )}
              </form.Field>

              {/* Log format — JSON is what makes the two levels settable, and
                  Lambda rejects either of them alongside Text. The hint sits
                  under the whole row rather than in the format field's own
                  column: it describes all three, and a 200px column wraps it
                  to three cramped lines. */}
              <div className="flex flex-col gap-1.5">
                <FormRow>
                  <FormField label="Log format">
                    <Select
                      value={logFormat}
                      onChange={(e) =>
                        setLogFormat(e.target.value as LoggingConfigForm["logFormat"])
                      }
                    >
                      {LOG_FORMATS.map((format) => (
                        <option key={format} value={format}>
                          {format}
                        </option>
                      ))}
                    </Select>
                  </FormField>
                  {logFormat === "JSON" && (
                    <>
                      <FormField label="Application log level">
                        <Select
                          value={applicationLogLevel}
                          onChange={(e) =>
                            setApplicationLogLevel(
                              e.target.value as LoggingConfigForm["applicationLogLevel"],
                            )
                          }
                        >
                          {APPLICATION_LOG_LEVELS.map((level) => (
                            <option key={level} value={level}>
                              {level}
                            </option>
                          ))}
                        </Select>
                      </FormField>
                      <FormField label="System log level">
                        <Select
                          value={systemLogLevel}
                          onChange={(e) =>
                            setSystemLogLevel(e.target.value as LoggingConfigForm["systemLogLevel"])
                          }
                        >
                          {SYSTEM_LOG_LEVELS.map((level) => (
                            <option key={level} value={level}>
                              {level}
                            </option>
                          ))}
                        </Select>
                      </FormField>
                    </>
                  )}
                </FormRow>
                <p className="text-2xs text-fg-subtle">
                  {logFormat === "JSON"
                    ? "Platform records are emitted as JSON events and both streams are filtered at the selected level."
                    : "Plain text START / END / REPORT lines, unfiltered."}
                </p>
              </div>

              {/* ── General configuration (collapsible) ─────────────────────── */}
              <CollapsibleSection
                open={showAdvanced}
                onToggle={() => setShowAdvanced((v) => !v)}
                icon={<Settings className="h-3.5 w-3.5" />}
                label="General configuration"
                summary={
                  <form.Subscribe selector={(s) => [s.values.memorySize, s.values.timeout]}>
                    {([mem, to]) => `${mem} MB · ${to}s timeout`}
                  </form.Subscribe>
                }
              >
                <div className="flex gap-3">
                  <form.Field name="memorySize">
                    {(field) => (
                      <div className="flex-1">
                        <FormField label="Memory (MB)" error={fieldError(field.state.meta.errors)}>
                          <Input
                            type="number"
                            min={128}
                            max={10240}
                            step={64}
                            value={field.state.value}
                            onChange={(e) => field.handleChange(Number(e.target.value))}
                          />
                        </FormField>
                      </div>
                    )}
                  </form.Field>
                  <form.Field name="timeout">
                    {(field) => (
                      <div className="flex-1">
                        <FormField
                          label="Timeout (seconds)"
                          error={fieldError(field.state.meta.errors)}
                        >
                          <Input
                            type="number"
                            min={1}
                            max={900}
                            value={field.state.value}
                            onChange={(e) => field.handleChange(Number(e.target.value))}
                          />
                        </FormField>
                      </div>
                    )}
                  </form.Field>
                </div>
              </CollapsibleSection>

              {/* ── Environment variables (collapsible) ─────────────────────── */}
              <CollapsibleSection
                open={showEnvVars}
                onToggle={() => setShowEnvVars((v) => !v)}
                icon={<Variable className="h-3.5 w-3.5" />}
                label="Environment variables"
                summary={
                  envVars.length > 0 ? formatQuantity(envVars.length, "variable") : undefined
                }
              >
                <div className="flex flex-col gap-2">
                  {envVars.map((entry, i) => (
                    <div key={i} className="flex items-start gap-2">
                      <Input
                        className="flex-1 font-mono text-xs"
                        value={entry.key}
                        onChange={(e) => {
                          const next = [...envVars]
                          next[i] = { ...next[i], key: e.target.value }
                          setEnvVars(next)
                        }}
                        placeholder="KEY"
                      />
                      <Input
                        className="flex-1 font-mono text-xs"
                        value={entry.value}
                        onChange={(e) => {
                          const next = [...envVars]
                          next[i] = { ...next[i], value: e.target.value }
                          setEnvVars(next)
                        }}
                        placeholder="value"
                      />
                      <Button
                        variant="ghost"
                        size="sm"
                        type="button"
                        className="shrink-0 text-danger hover:text-danger"
                        onClick={() => setEnvVars(envVars.filter((_, j) => j !== i))}
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    </div>
                  ))}
                  <div className="flex gap-2 self-start">
                    <Button
                      variant="ghost"
                      size="sm"
                      type="button"
                      onClick={() => setEnvVars([...envVars, { key: "", value: "" }])}
                    >
                      <Plus className="mr-1 h-3.5 w-3.5" />
                      Add variable
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      type="button"
                      // Reading the clipboard has no fallback: `execCommand("paste")`
                      // is refused everywhere, and Firefox never exposes `readText`
                      // to page scripts. Where it is unavailable the control says
                      // so rather than looking live and doing nothing.
                      disabled={!canPasteEnv}
                      title={
                        canPasteEnv
                          ? "Paste KEY=value lines from the clipboard"
                          : "This browser does not allow reading the clipboard — paste into the fields above instead"
                      }
                      onClick={async () => {
                        try {
                          const text = await readClipboardText()
                          const parsed = parseEnvString(text)
                          if (parsed.length > 0) {
                            setEnvVars((prev) => {
                              const merged = [...prev]
                              for (const entry of parsed) {
                                const idx = merged.findIndex((e) => e.key === entry.key)
                                if (idx >= 0) {
                                  merged[idx] = entry
                                } else {
                                  merged.push(entry)
                                }
                              }
                              return merged
                            })
                            if (!showEnvVars) setShowEnvVars(true)
                          }
                        } catch {
                          // Clipboard access denied — ignore
                        }
                      }}
                    >
                      <ClipboardPaste className="mr-1 h-3.5 w-3.5" />
                      Paste .env
                    </Button>
                  </div>
                </div>
              </CollapsibleSection>
            </div>
          )}

          {/* ─── Step 2: Code source (Zip) ───────────────────────────────────── */}
          {step === 2 && pkgType === "Zip" && (
            <div className="flex flex-col gap-3">
              {/* Source tabs */}
              <div className="flex gap-2">
                <WizardOptionCard
                  active={codeSource === "inline"}
                  icon={<Code2 className="h-4 w-4" />}
                  label="Write inline"
                  description="Write code directly in the browser"
                  onClick={() => setCodeSource("inline")}
                />
                <WizardOptionCard
                  active={codeSource === "zip"}
                  icon={<Archive className="h-4 w-4" />}
                  label="Upload .zip"
                  description="Upload a .zip archive"
                  onClick={() => setCodeSource("zip")}
                />
                <WizardOptionCard
                  active={codeSource === "s3"}
                  icon={<Cloud className="h-4 w-4" />}
                  label="From S3"
                  description="Deploy from an S3 bucket"
                  onClick={() => setCodeSource("s3")}
                />
              </div>

              {/* Inline editor */}
              {codeSource === "inline" && (
                <div className="flex flex-col gap-1.5">
                  <div className="flex items-center gap-2 text-xs text-fg-muted">
                    <span className="font-mono">{def.filename}</span>
                    <span>·</span>
                    <span className="capitalize">{runtimeLanguage(runtime)}</span>
                  </div>
                  <div className="overflow-hidden rounded-md border border-border">
                    <Editor
                      height="35vh"
                      language={runtimeLanguage(runtime)}
                      value={editorValue}
                      theme={monacoTheme(isDark)}
                      onChange={(val) => setEditorValue(val ?? "")}
                      options={{ ...MONACO_BASE_OPTIONS, padding: { top: 12, bottom: 12 } }}
                    />
                  </div>
                </div>
              )}

              {/* Zip upload */}
              {codeSource === "zip" && (
                <ZipDropzone
                  file={zipFile}
                  onChange={setZipFile}
                  description="Select a .zip file containing your function code"
                />
              )}

              {/* S3 source */}
              {codeSource === "s3" && (
                <ZipS3SourceFields
                  bucket={s3Bucket}
                  onBucket={setS3Bucket}
                  s3Key={s3Key}
                  onS3Key={setS3Key}
                  objectVersion={s3ObjectVersion}
                  onObjectVersion={setS3ObjectVersion}
                  bucketPlaceholder="my-deployment-bucket"
                  keyPlaceholder="functions/my-function.zip"
                />
              )}

              {/* Optional KMS encryption — available for zip and s3 */}
              {(codeSource === "zip" || codeSource === "s3") && (
                <div className="mt-1">
                  <button
                    type="button"
                    className="text-xs text-fg-muted transition-colors hover:text-fg"
                    onClick={() => setShowKMS((v) => !v)}
                  >
                    {showKMS ? "▾" : "▸"} Encryption (optional)
                  </button>
                  {showKMS && (
                    <div className="mt-2">
                      <FormRow>
                        <FormField
                          label="Source KMS key ARN"
                          hint="Optional — encrypts the deployment package"
                        >
                          <Input
                            value={sourceKMSKeyArn}
                            onChange={(e) => setSourceKMSKeyArn(e.target.value)}
                            placeholder="arn:aws:kms:us-east-1:123456789012:key/…"
                          />
                        </FormField>
                      </FormRow>
                    </div>
                  )}
                </div>
              )}
            </div>
          )}

          {/* ─── Step 2: Image — SourceKMSKeyArn only ───────────────────────── */}
          {step === 2 && pkgType === "Image" && (
            <div className="flex flex-col gap-3">
              <p className="text-sm text-fg-muted">
                Your function will be deployed from{" "}
                <form.Subscribe selector={(s) => s.values.imageUri}>
                  {(uri) => <span className="font-mono text-fg">{uri}</span>}
                </form.Subscribe>
                .
              </p>
              <FormRow>
                <FormField
                  label="Source KMS key ARN"
                  hint="Optional — encrypts the container image"
                >
                  <Input
                    value={sourceKMSKeyArn}
                    onChange={(e) => setSourceKMSKeyArn(e.target.value)}
                    placeholder="arn:aws:kms:us-east-1:123456789012:key/…"
                  />
                </FormField>
              </FormRow>
            </div>
          )}
        </DialogBody>

        {/* ─── Footer ──────────────────────────────────────────────────────── */}
        <DialogFooter className="mt-2">
          {step === 1 && (
            <>
              <Button variant="ghost" type="button" onClick={() => handleOpenChange(false)}>
                Cancel
              </Button>
              <form.Subscribe
                selector={(s) => ({
                  fn: s.values.functionName,
                  role: s.values.role,
                  runtime: s.values.runtime,
                  handler: s.values.handler,
                  imageUri: s.values.imageUri,
                })}
              >
                {(v) => {
                  const canAdvance =
                    !!v.fn.trim() &&
                    !!v.role.trim() &&
                    (pkgType === "Zip" ? !!v.runtime && !!v.handler.trim() : !!v.imageUri.trim())
                  return (
                    <Button type="button" onClick={handleAdvance} disabled={!canAdvance}>
                      Next
                      <ChevronRight className="ml-1 h-4 w-4" />
                    </Button>
                  )
                }}
              </form.Subscribe>
            </>
          )}
          {step === 2 && (
            <>
              <Button variant="ghost" type="button" onClick={() => setStep(1)}>
                <ChevronLeft className="mr-1 h-4 w-4" />
                Back
              </Button>
              <Button type="button" onClick={handleCreate} disabled={isCreating || !canCreate}>
                {isCreating ? (
                  <>
                    <Spinner className="mr-2 h-3.5 w-3.5" />
                    Creating…
                  </>
                ) : (
                  "Create function"
                )}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ─── Collapsible section ─────────────────────────────────────────────────────

function CollapsibleSection({
  open,
  onToggle,
  icon,
  label,
  summary,
  children,
}: {
  open: boolean
  onToggle: () => void
  icon: React.ReactNode
  label: string
  summary?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div className="rounded-md border border-border">
      <button
        type="button"
        onClick={onToggle}
        className="flex w-full items-center gap-2 px-3 py-2.5 text-sm transition-colors hover:bg-accent-muted"
      >
        {icon}
        <span className="font-medium">{label}</span>
        {!open && summary && <span className="ml-auto text-xs text-fg-muted">{summary}</span>}
        <span className={cn("ml-auto text-fg-muted transition-transform", open && "rotate-180")}>
          ▾
        </span>
      </button>
      {open && <div className="border-t border-border px-3 py-3">{children}</div>}
    </div>
  )
}
