<script lang="ts">
  import { resolve } from '$app/paths';
  import MeetingStage from '#lib/MeetingStage.svelte';
  import NewRoomCard from '#lib/NewRoomCard.svelte';
  import SiteFooter from '#lib/SiteFooter.svelte';
  import SiteHeader from '#lib/SiteHeader.svelte';
  import TideBackdrop from '#lib/TideBackdrop.svelte';
</script>

<svelte:head>
  <title>tide · Simple, self-hostable video conference service</title>
  <meta name="description" content="Simple, self-hostable video conference service." />
</svelte:head>

<div class="flex min-h-dvh flex-col xl:h-dvh xl:min-h-[40rem]">
  <SiteHeader />
  <main class="flex min-h-0 flex-1 flex-col px-4 sm:px-6">
    <section
      class="relative grid min-h-0 flex-1 content-center items-center gap-12 overflow-hidden rounded-lg border border-line bg-stone px-5 py-10 sm:px-10 xl:grid-cols-[minmax(0,26rem)_minmax(0,1fr)] xl:gap-16 xl:px-14"
    >
      <TideBackdrop />
      <div class="relative">
        <h1 class="text-[30px] leading-[1.12] tracking-[-0.02em] text-balance sm:text-[44px]">
          tide. <span class="text-ink-2">Simple, self&#8209;hostable video conference service</span>
        </h1>
        <div class="mt-8 flex flex-wrap gap-2">
          <a class="button button-primary" href={resolve('/docs/[slug]', { slug: 'prepare' })}
            >Deploy</a
          >
          <a class="button" href={resolve('/docs/[slug]', { slug: 'local' })}>Run locally</a>
        </div>
      </div>

      <!-- The product, as it looks: a room being created, and the meeting it opens. -->
      <div class="relative mx-auto w-full max-w-[52rem] pt-16 pb-2 sm:pt-24 xl:pt-20">
        <div class="absolute top-0 left-0 w-[26rem] -rotate-1 xl:-left-[8%]">
          <NewRoomCard />
        </div>
        <div class="relative ml-auto w-[94%] sm:w-[80%]">
          <MeetingStage />
        </div>
      </div>
    </section>
  </main>
  <SiteFooter />
</div>
