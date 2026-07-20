import { play } from 'cuelume';

export function playNewMessageSound(): void {
  play('ready');
  play('tick');
}

export function playRecordingStartedSound(): void {
  play('chime');
}

export function playParticipantEnteredSound(): void {
  play('chime', { transpose: -12 });
}

export function playParticipantExitedSound(): void {
  play('error');
}

export function playLobbyKnockSound(): void {
  play('ready', { transpose: -5 });
  setTimeout(() => play('ready', { transpose: -12 }), 300);
}
