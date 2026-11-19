import { Dialog as DialogPrimitive } from "@base-ui/react/dialog"
import { cn } from "@cozystack/ui"

const DialogRoot = DialogPrimitive.Root

const DialogPortal = DialogPrimitive.Portal

const DialogClose = DialogPrimitive.Close

function DialogBackdrop({
  className,
  ...props
}: DialogPrimitive.Backdrop.Props) {
  return (
    <DialogPrimitive.Backdrop
      data-slot="dialog-backdrop"
      className={cn(
        "fixed inset-0 z-50 bg-black/25 backdrop-blur-sm transition-opacity duration-150 data-[open]:opacity-100 data-[closed]:opacity-0",
        className
      )}
      {...props}
    />
  )
}

function DialogPopup({
  className,
  children,
  ...props
}: DialogPrimitive.Popup.Props) {
  return (
    <DialogPrimitive.Popup
      data-slot="dialog-popup"
      className={cn(
        "fixed top-[14%] left-1/2 z-50 -translate-x-1/2 rounded-xl bg-white text-slate-900 shadow-[0_20px_60px_rgba(0,0,0,0.18)] ring-1 ring-slate-900/8 outline-none transition-all duration-150",
        className
      )}
      {...props}
    >
      {children}
    </DialogPrimitive.Popup>
  )
}

function DialogTitle({
  className,
  ...props
}: DialogPrimitive.Title.Props) {
  return (
    <DialogPrimitive.Title
      data-slot="dialog-title"
      className={cn("text-lg font-semibold", className)}
      {...props}
    />
  )
}

function DialogDescription({
  className,
  ...props
}: DialogPrimitive.Description.Props) {
  return (
    <DialogPrimitive.Description
      data-slot="dialog-description"
      className={cn("text-sm text-slate-500", className)}
      {...props}
    />
  )
}

export {
  DialogRoot,
  DialogPortal,
  DialogBackdrop,
  DialogPopup,
  DialogTitle,
  DialogDescription,
  DialogClose,
}
