import { useState } from "react"
import { ChevronDown, ChevronRight } from "lucide-react"
import { useSelfSubjectAccessReview } from "@cozystack/k8s-client"
import { Button, Section, Spinner } from "@cozystack/ui"
import { CredentialActionDialog } from "./CredentialActionDialog.tsx"
import { formatTime, originExplanation, originLabel } from "./credential-origin.ts"
import {
  CREDENTIALS_GROUP,
  type Credential,
  type CredentialOperation,
} from "./use-app-credentials.ts"

// Fail closed: while the review loads or errors the actions stay hidden.
function useOperationAllowed(namespace: string, operation: CredentialOperation): boolean {
  const review = useSelfSubjectAccessReview({
    resourceAttributes: {
      namespace,
      group: CREDENTIALS_GROUP,
      resource: "credentials",
      subresource: operation,
      verb: "create",
    },
  })
  return !review.isLoading && !review.error && review.allowed
}

function History({ credential }: { credential: Credential }) {
  const [open, setOpen] = useState(false)
  const entries = credential.status?.history ?? []
  if (entries.length === 0) return null
  return (
    <div>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-1 text-xs text-slate-500 hover:text-slate-800"
      >
        {open ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
        History ({entries.length})
      </button>
      {open && (
        <ul className="mt-2 space-y-1 text-xs text-slate-600">
          {entries.map((entry, i) => (
            <li key={`${entry.at}-${i}`}>
              <span className="font-medium">{entry.operation}</span> by {entry.by || "unknown"} at {formatTime(entry.at)}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function CredentialRow({
  credential,
  canMint,
  canRevoke,
  onAction,
}: {
  credential: Credential
  canMint: boolean
  canRevoke: boolean
  onAction: (operation: CredentialOperation) => void
}) {
  const user = credential.spec?.user ?? credential.metadata.name
  const password = credential.status?.password
  return (
    <div className="space-y-2 px-5 py-3 text-sm">
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-1">
          <p><code className="font-medium">{user}</code> <span className="ml-2 text-xs text-slate-500">{originLabel(credential)}</span></p>
          <p className="text-slate-600">{originExplanation(credential)}</p>
          <p className="text-xs text-slate-500">
            Last change: {formatTime(password?.at)}{password?.by ? ` by ${password.by}` : ""}
          </p>
        </div>
        {(canMint || canRevoke) && (
          <div className="flex shrink-0 gap-2">
            {canMint && (
              <Button size="sm" variant="outline" onClick={() => onAction("mint")} aria-label={`Generate password for ${user}`}>
                Generate password
              </Button>
            )}
            {canRevoke && password?.origin !== "Revoked" && password?.origin !== "NotIssued" && (
              <Button size="sm" variant="outline" onClick={() => onAction("revoke")} aria-label={`Revoke password for ${user}`}>
                Revoke
              </Button>
            )}
          </div>
        )}
      </div>
      <History credential={credential} />
    </div>
  )
}

export function CredentialsTab({
  namespace,
  credentials,
  isLoading,
  error,
  onChanged,
}: {
  namespace: string
  credentials: Credential[]
  isLoading: boolean
  error: Error | null
  onChanged: () => void
}) {
  const canMint = useOperationAllowed(namespace, "mint")
  const canRevoke = useOperationAllowed(namespace, "revoke")
  const [action, setAction] = useState<{ credential: Credential; operation: CredentialOperation } | null>(null)

  return (
    <div className="space-y-6 p-6">
      <Section
        title="Credentials"
        description="Passwords are never stored by the console. A generated password is shown once, to you."
        bodyClassName="p-0"
      >
        {error ? <p role="alert" className="p-4 text-red-600">{error.message}</p>
          : isLoading ? <div className="flex items-center gap-2 p-4"><Spinner /> Loading…</div>
          : credentials.length === 0 ? <p className="p-4">No accounts yet.</p>
          : (
            <div className="divide-y divide-slate-100">
              {credentials.map((credential) => (
                <CredentialRow
                  key={credential.metadata.name}
                  credential={credential}
                  canMint={canMint}
                  canRevoke={canRevoke}
                  onAction={(operation) => setAction({ credential, operation })}
                />
              ))}
            </div>
          )}
      </Section>
      {action && (
        <CredentialActionDialog
          key={`${action.credential.metadata.name}/${action.operation}`}
          namespace={namespace}
          credential={action.credential}
          operation={action.operation}
          onChanged={onChanged}
          onClose={() => setAction(null)}
        />
      )}
    </div>
  )
}
