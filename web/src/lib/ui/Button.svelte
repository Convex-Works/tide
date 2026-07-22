<script lang="ts">
  import { Button } from 'bits-ui';

  // Standardized pill button. Renders an <a> when `href` is set, otherwise a
  // <button> — bits-ui Button handles the anchor/button + disabled semantics.
  //   variant="default"  paper pill, subtle border (Copy, secondary actions)
  //   variant="accent"   solid accent pill, white text (Join — primary action)
  // Pass `class` to extend (e.g. `w-full` for a block button).
  type Variant = 'default' | 'accent';

  let {
    variant = 'default',
    class: className = '',
    children,
    ...rest
  }: Button.RootProps & { variant?: Variant } = $props();

  // Geometry is pinned so nothing depends on the 13px root font-size that would
  // otherwise re-scale rem-based utilities. `border` also neutralizes the native
  // <button> UA outset border (Preflight is intentionally off — see app.css).
  const base =
    'inline-flex h-10 items-center justify-center gap-1 rounded-full border px-5.5 text-base font-medium no-underline transition-colors disabled:opacity-60 disabled:pointer-events-none';

  const variants: Record<Variant, string> = {
    default: 'border-border bg-paper text-ink-2 hover:bg-stone-200 hover:text-ink',
    accent: 'border-accent bg-accent text-white hover:border-accent-hover hover:bg-accent-hover'
  };
</script>

<Button.Root class={`${base} ${variants[variant]} ${className}`.trim()} {...rest}>
  {@render children?.()}
</Button.Root>
