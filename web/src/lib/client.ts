import { createClient, Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { PetService } from "../gen/pet/v2/pet_pb";

export function getApiBaseUrl(): string {
  if (typeof process !== "undefined" && process.env?.VITE_API_URL) {
    return process.env.VITE_API_URL;
  }
  if (typeof import.meta !== "undefined" && import.meta.env?.VITE_API_URL) {
    return import.meta.env.VITE_API_URL;
  }
  if (typeof window !== "undefined") {
    // When running in the browser, default to the current origin so Vite dev server proxies
    // requests seamlessly to the configured backend target without CORS restrictions.
    return window.location.origin;
  }
  return "https://localhost:8080";
}

export function createTransport(baseUrl: string = getApiBaseUrl()): Transport {
  return createConnectTransport({
    baseUrl,
    fetch: (input, init) => {
      // Include browser session credentials/cookies by default for upstream IAP / OAuth proxy
      return fetch(input, { ...init, credentials: "include" });
    },
  });
}

export const transport = createTransport();

export function getPetClient(customTransport?: Transport) {
  return createClient(PetService, customTransport || transport);
}

