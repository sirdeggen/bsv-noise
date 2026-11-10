import { PeerPayClient } from "@bsv/message-box-client";
import { Transaction, WalletClient } from "@bsv/sdk";

export interface FundTarget {
  identityKey: string;
  messageBox: string;
  messageBoxURL: string;
  originator: string;
  satoshis: number;
}

/**
 * Pay the service identity through PeerPay.
 *
 * WalletClient talks to the operator's local BRC-100 wallet (BSV Desktop).
 * PeerPayClient derives the BRC-29 output and posts the payment token to
 * payment_inbox on the configured Message Box. One token is created and sent
 * once, so the wallet is not asked to build a second transaction.
 */
export async function fundService(target: FundTarget): Promise<string> {
  const originator = target.originator || "brc29-synthetic-load.local";
  const wallet = new WalletClient("auto", originator);
  const peer = new PeerPayClient({
    walletClient: wallet,
    messageBoxHost: target.messageBoxURL,
    messageBox: target.messageBox || "payment_inbox",
    originator,
  });
  const token = await peer.createPaymentToken({
    recipient: target.identityKey,
    amount: target.satoshis,
  });
  // id("hex") is the Bitcoin/WhatsOnChain display txid (byte-reversed hash256).
  // hash("hex") is the internal byte order and 404s on explorers.
  const txid = Transaction.fromAtomicBEEF(token.transaction).id("hex");
  if (typeof txid !== "string" || txid.length !== 64) {
    throw new Error("The wallet did not return a transaction id.");
  }
  await peer.sendMessage(
    {
      recipient: target.identityKey,
      messageBox: target.messageBox || "payment_inbox",
      body: JSON.stringify(token),
    },
    target.messageBoxURL,
  );
  return txid;
}
