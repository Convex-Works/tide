<script lang="ts">
  import type { ParticipantView } from './media';
  import { attachMediaTrack } from './mediaElement';

  let { participants }: { participants: ParticipantView[] } = $props();

  let audioPublications = $derived(
    participants.flatMap((participant) =>
      participant.isLocal
        ? []
        : Object.values(participant.audio)
            // Keep the sink through remote mute/SFU pause. The publication
            // still owns this element, and LiveKit resumes it in place.
            .filter(
              (publication) =>
                publication.subscribed && publication.permissionAllowed && publication.track
            )
            .map((publication) => ({ participant, publication }))
    )
  );
</script>

<div class="remote-audio" aria-hidden="true" data-testid="remote-audio-renderer">
  {#each audioPublications as item (item.publication.publicationSid)}
    <!-- Live meeting audio does not have a caption track. -->
    <audio
      use:attachMediaTrack={item.publication}
      autoplay
      data-participant-identity={item.participant.identity}
      aria-label={`${item.participant.name}'s audio`}
    ></audio>
  {/each}
</div>

<style>
  .remote-audio {
    position: absolute;
    width: 0;
    height: 0;
    overflow: hidden;
    pointer-events: none;
  }
</style>
