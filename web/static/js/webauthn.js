/* ── WebAuthn plumbing ────────────────────────────────────────────────
 *
 * The browser API speaks ArrayBuffers and the wire speaks base64url, so every
 * ceremony is a pair of conversions around one call. Kept in one place because
 * getting a single field wrong fails inside the browser with a message that
 * says nothing useful.
 */
const b64urlToBuf = s => {
  const pad = '='.repeat((4 - (s.length % 4)) % 4);
  const bin = atob((s + pad).replace(/-/g, '+').replace(/_/g, '/'));
  return Uint8Array.from(bin, c => c.charCodeAt(0)).buffer;
};

const bufToB64url = buf =>
  btoa(String.fromCharCode(...new Uint8Array(buf)))
    .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');

export function passkeysSupported() {
  return typeof window.PublicKeyCredential === 'function' &&
    typeof navigator.credentials?.create === 'function';
}

/* Turns the server's JSON options into what navigator.credentials wants. */
function decodeCreationOptions(o) {
  const p = o.publicKey || o;
  return {
    publicKey: {
      ...p,
      challenge: b64urlToBuf(p.challenge),
      user: { ...p.user, id: b64urlToBuf(p.user.id) },
      excludeCredentials: (p.excludeCredentials || []).map(c => ({ ...c, id: b64urlToBuf(c.id) })),
    },
  };
}

function decodeRequestOptions(o) {
  const p = o.publicKey || o;
  return {
    publicKey: {
      ...p,
      challenge: b64urlToBuf(p.challenge),
      allowCredentials: (p.allowCredentials || []).map(c => ({ ...c, id: b64urlToBuf(c.id) })),
    },
  };
}

export async function createPasskey(options) {
  const cred = await navigator.credentials.create(decodeCreationOptions(options));
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    // Some authenticators report how they can be reached; the server stores it
    // so the next prompt can be narrowed to the right kind of key.
    transports: cred.response.getTransports ? cred.response.getTransports() : [],
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      attestationObject: bufToB64url(cred.response.attestationObject),
      clientDataJSON: bufToB64url(cred.response.clientDataJSON),
    },
  };
}

export async function getPasskey(options) {
  const cred = await navigator.credentials.get(decodeRequestOptions(options));
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      authenticatorData: bufToB64url(cred.response.authenticatorData),
      clientDataJSON: bufToB64url(cred.response.clientDataJSON),
      signature: bufToB64url(cred.response.signature),
      userHandle: cred.response.userHandle ? bufToB64url(cred.response.userHandle) : null,
    },
  };
}
