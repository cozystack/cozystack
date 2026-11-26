import { useState } from "react"
import { K8sApiError, useK8sClient } from "@cozystack/k8s-client"
import { Button, Spinner } from "@cozystack/ui"
import {
  DialogBackdrop,
  DialogDescription,
  DialogPopup,
  DialogPortal,
  DialogRoot,
  DialogTitle,
} from "../../components/ui/dialog.tsx"
import {
  runCredentialOperation,
  type Credential,
  type CredentialOperation,
} from "./use-app-credentials.ts"

type Phase =
  | { name: "confirm" }
  | { name: "running" }
  | { name: "failed"; message: string }
  | { name: "minted"; username: string; password: string }

function failureMessage(error: unknown, operation: CredentialOperation): string {
  if (error instanceof K8sApiError && error.status === 409) {
    return `${error.message}. The list has been refreshed: check the account and try again.`
  }
  if (error instanceof K8sApiError && error.status === 403) {
    return `You are not allowed to ${operation} this password: ${error.message}`
  }
  return error instanceof Error ? error.message : "The request failed."
}

function MintedPassword({ username, password }: { username: string; password: string }) {
  const [copy, setCopy] = useState<"idle" | "ok" | "failed">("idle")
  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(password)
      setCopy("ok")
    } catch {
      setCopy("failed")
    }
  }
  return (
    <div className="space-y-3">
      <p role="alert" className="rounded-md bg-amber-50 p-3 text-sm text-amber-900">
        Save this password now. It cannot be shown again: the platform does not keep it.
      </p>
      <dl className="space-y-2 text-sm">
        <div className="flex gap-2"><dt className="w-20 text-slate-500">Username</dt><dd><code>{username}</code></dd></div>
        <div className="flex items-start gap-2">
          <dt className="w-20 pt-1 text-slate-500">Password</dt>
          <dd className="flex-1"><code data-testid="minted-password" className="block break-all rounded bg-slate-900 p-2 text-xs text-slate-100">{password}</code></dd>
        </div>
      </dl>
      <div className="flex items-center gap-2">
        <Button size="sm" variant="outline" disabled={!navigator.clipboard} onClick={handleCopy}>Copy password</Button>
        {copy !== "idle" && (
          <span role="status" className="text-xs">
            {copy === "ok" ? "Copied." : "Could not copy. Select and copy the password manually."}
          </span>
        )}
      </div>
    </div>
  )
}

export function CredentialActionDialog({
  namespace,
  credential,
  operation,
  onChanged,
  onClose,
}: {
  namespace: string
  credential: Credential
  operation: CredentialOperation
  onChanged: () => void
  onClose: () => void
}) {
  const client = useK8sClient()
  const [phase, setPhase] = useState<Phase>({ name: "confirm" })
  const user = credential.spec?.user ?? credential.metadata.name
  const verb = operation === "mint" ? "Generate password" : "Revoke"

  const close = () => {
    // Drop the password before the dialog goes away, not only on unmount.
    setPhase({ name: "confirm" })
    onClose()
  }

  const run = async () => {
    setPhase({ name: "running" })
    try {
      const result = await runCredentialOperation(client, namespace, credential, operation)
      onChanged()
      if (operation === "mint" && result?.status?.password) {
        setPhase({ name: "minted", username: result.status.username ?? user, password: result.status.password })
      } else {
        onClose()
      }
    } catch (error) {
      if (error instanceof K8sApiError && error.status === 409) onChanged()
      setPhase({ name: "failed", message: failureMessage(error, operation) })
    }
  }

  return (
    <DialogRoot
      open
      modal
      disablePointerDismissal={phase.name === "minted" || phase.name === "running"}
      onOpenChange={(open) => { if (!open && phase.name !== "running") close() }}
    >
      <DialogPortal>
        <DialogBackdrop />
        <DialogPopup className="w-[min(32rem,calc(100vw-2rem))] space-y-4 p-5">
          {phase.name === "minted" ? (
            <>
              <DialogTitle>New password for {user}</DialogTitle>
              <MintedPassword username={phase.username} password={phase.password} />
              <div className="flex justify-end">
                <Button size="sm" onClick={close}>I have saved it, close</Button>
              </div>
            </>
          ) : (
            <>
              <DialogTitle>{verb} for {user}?</DialogTitle>
              <DialogDescription>
                {operation === "mint"
                  ? "A new password is generated and shown to you once. The current password stops working for every client that uses it."
                  : "The current password stops working for every client that uses it, and nobody holds a working one until a new password is generated."}
              </DialogDescription>
              {phase.name === "failed" && (
                <p role="alert" className="rounded-md bg-red-50 p-3 text-sm text-red-700">{phase.message}</p>
              )}
              <div className="flex justify-end gap-2">
                <Button size="sm" variant="outline" disabled={phase.name === "running"} onClick={close}>
                  {phase.name === "failed" ? "Close" : "Cancel"}
                </Button>
                {phase.name !== "failed" && (
                  <Button
                    size="sm"
                    variant={operation === "revoke" ? "destructive" : "default"}
                    disabled={phase.name === "running"}
                    onClick={run}
                  >
                    {phase.name === "running" && <Spinner />} {verb}
                  </Button>
                )}
              </div>
            </>
          )}
        </DialogPopup>
      </DialogPortal>
    </DialogRoot>
  )
}
