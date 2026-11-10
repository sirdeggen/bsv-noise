export interface RecentTx {
  id: string;
  txid: string;
  amount_satoshis: number;
  fee_satoshis: number;
  state: string;
  dry_run: boolean;
  explorer_url?: string;
  created: string;
}

export interface Stats {
  synthetic: boolean;
  health: string;
  ready: boolean;
  paused?: string;
  mode: string;
  network: string;
  dry_run: boolean;
  broadcast: boolean;
  import_funding: boolean;
  identity_key: string;
  message_box: string;
  message_box_url: string;
  originator: string;
  balance_satoshis: number;
  balance_bsv: string;
  transactions: {
    total: number;
    broadcast: number;
    dry_run: number;
    confirmed: number;
    seen: number;
    failed: number;
    in_flight: number;
  };
  tps: {
    target: number;
    attempted: number;
    accepted: number;
    confirmed: number;
    failed: number;
    interval_variance_seconds2: number;
    exponential_variance_seconds2: number;
    sample_gaps: number;
    state: string;
  };
  funding: {
    pending: number;
    acknowledged: number;
    failed: number;
    last_sweep?: string | null;
    last_error?: string;
  };
  recent: RecentTx[];
  note: string;
}
