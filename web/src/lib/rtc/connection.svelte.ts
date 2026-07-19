export type HairlineState = 'connected' | 'reconnecting' | 'offline';

class ConnectionChromeState {
  state = $state<HairlineState>('connected');
}

export const connectionChrome = new ConnectionChromeState();

export function setConnectionChrome(state: HairlineState): void {
  connectionChrome.state = state;
}
