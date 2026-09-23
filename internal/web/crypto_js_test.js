// Checks the browser's cryptography against the published vectors and against
// the Go implementation it has to agree with. Run by crypto_test.go when node
// is available; standalone with `node internal/web/crypto_js_test.js`.
//
// Hand-written cryptography that has not been checked against published
// vectors is cryptography nobody should trust, and two implementations that
// have not been checked against each other will eventually disagree — silently,
// and at the moment somebody is locked out of their own box.

const path = require('path');
const c = require(path.join(__dirname, 'static', 'crypto.js'));

let failed = 0;
function check(name, got, want) {
  if (got !== want) {
    failed++;
    console.log(`FAIL ${name}\n  got  ${got}\n  want ${want}`);
  } else {
    console.log(`ok   ${name}`);
  }
}

// --- FIPS 180-4 and the padding boundaries, where this usually goes wrong ---
check('sha256 empty', c.toHex(c.sha256(c.utf8(''))),
  'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855');
check('sha256 abc', c.toHex(c.sha256(c.utf8('abc'))),
  'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad');
check('sha256 two blocks', c.toHex(c.sha256(c.utf8('abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq'))),
  '248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1');
check('sha256 55 bytes', c.toHex(c.sha256(c.utf8('a'.repeat(55)))),
  '9f4390f8d30c2dd92ec9f095b65e2b9ae9b0a925a5258e241c9f1e910f734318');
check('sha256 56 bytes', c.toHex(c.sha256(c.utf8('a'.repeat(56)))),
  'b35439a4ac6f0948b6d6f9e3c6af0f5f590ce20f1bde7090ef7970686ec6738a');
check('sha256 64 bytes', c.toHex(c.sha256(c.utf8('a'.repeat(64)))),
  'ffe054fe7ae0cb6dc65c3af9b61d5209f439851db43d0ba5997337df154668eb');

// --- RFC 4231 ---
check('hmac rfc4231 #1', c.toHex(c.hmacSha256(new Uint8Array(20).fill(0x0b), c.utf8('Hi There'))),
  'b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7');
check('hmac rfc4231 #2', c.toHex(c.hmacSha256(c.utf8('Jefe'), c.utf8('what do ya want for nothing?'))),
  '5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843');
check('hmac rfc4231 #6 long key', c.toHex(c.hmacSha256(new Uint8Array(131).fill(0xaa),
  c.utf8('Test Using Larger Than Block-Size Key - Hash Key First'))),
  '60e431591ee0b67f0d8a26aacbf5b77f8e0bc6213728c5140546040f0ee37f54');

// --- RFC 7914 §11 ---
check('pbkdf2 c=1', c.toHex(c.pbkdf2Sha256(c.utf8('passwd'), c.utf8('salt'), 1)).slice(0, 32),
  '55ac046e56e3089fec1691c22544b605');
check('pbkdf2 c=80000', c.toHex(c.pbkdf2Sha256(c.utf8('Password'), c.utf8('NaCl'), 80000)).slice(0, 32),
  '4ddcd8f60b98be21830cee5ef22701f9');

// --- The same numbers internal/auth/vectors_test.go checks ---
const cross = [
  {
    pin: '1234', salt: '0123456789abcdef', iterations: 1000, nonce: 'cafebabe',
    key: '96d86bc387622a132a8ec3fa3dbf14b7c25a903339de2db067430a08647c827e',
    proof: 'cDXyo8FCyFEZOGX8wzJeYaKfsqhUPi1pFPDFnycvzco=',
  },
  {
    pin: 'et lengre passord med mellomrom', salt: 'deadbeefdeadbeef', iterations: 2000, nonce: '0f0f0f0f',
    key: '4e61e6fad4d9bc49c55570a33e486caac275bf3791e36fcf37efc85ab78fa523',
    proof: '3nUHQfCZOzBmixBENBZ4ZUN0JJLErMdQzR+xMXhJaIw=',
  },
];
for (const v of cross) {
  const key = c.deriveKey(v.pin, v.salt, v.iterations);
  check(`go agreement, key for ${JSON.stringify(v.pin)}`, c.toHex(key), v.key);
  check(`go agreement, proof for ${JSON.stringify(v.pin)}`, c.proveNonce(key, v.nonce), v.proof);
}

// btoa is a browser global; node needs it for the base64 the proof uses.
process.exit(failed ? 1 : 0);
