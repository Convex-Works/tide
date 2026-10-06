<script lang="ts">
  import { resolve } from '$app/paths';
  import ArrowUpRight from 'phosphor-svelte/lib/ArrowUpRight';
  import Check from 'phosphor-svelte/lib/Check';
  import { demoURL } from '#lib/links.ts';
  import MeetingStage from '#lib/MeetingStage.svelte';
  import NewRoomCard from '#lib/NewRoomCard.svelte';
  import RotatingWords from '#lib/RotatingWords.svelte';
  import SiteFooter from '#lib/SiteFooter.svelte';
  import SiteHeader from '#lib/SiteHeader.svelte';
  import TideBackdrop from '#lib/TideBackdrop.svelte';

  const names = [
    'video conference service',
    'Jitsi alternative',
    'Zoom alternative',
    'Google Meet alternative'
  ];
  const features = [
    'One Go binary',
    'Media stays on your server',
    'Server-side recording',
    'OIDC sign-in'
  ];
</script>

<svelte:head>
  <title>tide · Self-hosted alternative to Jitsi, Zoom and Google Meet</title>
  <meta
    name="description"
    content="tide is a simple, self-hostable video conference service. One Go binary, server-side recording and OIDC sign-in. An alternative to Jitsi, Zoom and Google Meet that runs on your own server."
  />
</svelte:head>

<div class="flex min-h-dvh flex-col xl:h-dvh xl:min-h-[40rem]">
  <SiteHeader />
  <main class="flex min-h-0 flex-1 flex-col px-4 sm:px-6">
    <section
      class="relative grid min-h-0 flex-1 content-center items-center gap-12 overflow-hidden rounded-lg border border-line bg-white px-5 py-10 sm:px-10 xl:grid-cols-[minmax(0,30rem)_minmax(0,1fr)] xl:gap-16 xl:px-14"
    >
      <TideBackdrop />
      <div class="relative">
        <h1 class="text-[26px] leading-[1.12] tracking-[-0.02em] text-balance sm:text-[40px]">
          tide. <span class="text-ink-2">Simple, self&#8209;hostable</span>
          <RotatingWords words={names} />
        </h1>
        <ul class="mt-6 flex flex-wrap gap-x-5 gap-y-1.5 text-[14px]">
          {#each features as feature (feature)}
            <li class="flex items-center gap-2">
              <Check size={16} class="shrink-0 text-tide" />{feature}
            </li>
          {/each}
        </ul>
        <div class="mt-8 flex flex-wrap gap-2">
          <a class="group button button-primary" href={demoURL}
            >Try the demo<ArrowUpRight
              size={14}
              class="transition-transform group-hover:translate-x-px group-hover:-translate-y-px"
            /></a
          >
          <a class="button" href={resolve('/docs')}>Deploy</a>
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
