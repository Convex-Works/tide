<script lang="ts">
  import { Button } from 'bits-ui';

  // The shell's standard button (ARCHITECTURE.md §10): a 28px control with a
  // 4px radius. Renders an <a> when `href` is set, otherwise a <button> —
  // bits-ui Button handles the anchor/button + disabled semantics.
  //   variant="default"  paper, subtle border (Copy link, secondary actions)
  //   variant="accent"   solid accent, white text (Join meeting — primary action)
  // Pass `class` to extend (e.g. `w-full` for a block button).
  type Variant = 'default' | 'accent';

  let {
    variant = 'default',
    class: className = '',
    children,
    ...rest
  }: Button.RootProps & { variant?: Variant } = $props();

  // `border` also neutralizes the native <button> UA outset border (Preflight
  // is intentionally off — see app.css).
  const base =
    'inline-flex h-7 shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-control border px-2.5 text-[12px] font-[550] no-underline transition-colors disabled:opacity-60 disabled:pointer-events-none';

  const variants: Record<Variant, string> = {
    default: 'border-border bg-paper text-ink hover:bg-surface-2',
    accent: 'border-accent bg-accent text-white hover:border-accent-hover hover:bg-accent-hover'
  };
</script>

<Button.Root class={`${base} ${variants[variant]} ${className}`.trim()} {...rest}>
  {@render children?.()}
</Button.Root>
