export type HairlineState = 'connected' | 'reconnecting' | 'offline' | 'recording';

class ConnectionChromeState {
  state = $state<HairlineState>('connected');
  // Beside the state, not one of them: nothing else on the hairline may hide
  // that a program is listening (ARCHITECTURE.md §10).
  agent = $state(false);
}

export const connectionChrome = new ConnectionChromeState();

export function setConnectionChrome(state: HairlineState): void {
  connectionChrome.state = state;
}

export function setAgentChrome(present: boolean): void {
  connectionChrome.agent = present;
}

// Dev-only test hook: lets e2e specs drive the chrome deterministically.
if (import.meta.env.DEV && typeof window !== 'undefined') {
  (window as Window & { __tideChrome?: typeof setConnectionChrome }).__tideChrome =
    setConnectionChrome;
}
