import { Input as InputPrimitive } from "@base-ui/react/input"
import { cn } from "@cozystack/ui"

function Input({ className, ...props }: InputPrimitive.Props) {
  return (
    <InputPrimitive
      data-slot="input"
      className={cn(
        "h-7 w-full min-w-0 rounded-lg border border-slate-300 bg-white px-2.5 text-xs text-slate-900 shadow-xs outline-none transition-colors placeholder:text-slate-400 focus-visible:ring-2 focus-visible:ring-slate-400 focus-visible:ring-offset-2 data-[disabled]:pointer-events-none data-[disabled]:opacity-50",
        className
      )}
      {...props}
    />
  )
}

export { Input }
