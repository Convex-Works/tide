export type HairlineState = 'connected' | 'reconnecting' | 'offline' | 'recording';

class ConnectionChromeState {
  state = $state<HairlineState>('connected');
}

export const connectionChrome = new ConnectionChromeState();

export function setConnectionChrome(state: HairlineState): void {
  connectionChrome.state = state;
}

// Dev-only test hook: lets e2e specs drive the chrome deterministically.
if (import.meta.env.DEV && typeof window !== 'undefined') {
  (window as Window & { __tideChrome?: typeof setConnectionChrome }).__tideChrome =
    setConnectionChrome;
}
