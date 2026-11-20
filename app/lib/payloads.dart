import 'dart:convert';
import 'dart:typed_data';

/// The bytes the chip signs. The backend verifies the signature over these
/// exact bytes first and only then reads the fields, so key order is free.
///
/// A purpose is always present. Without it, a signature captured at login
/// would be just as valid at the payment endpoint.
Uint8List loginPayload({
  required String deviceId,
  required String keyId,
  required String nonce,
  required DateTime now,
}) => _encode({
  'purpose': 'session.login',
  'deviceId': deviceId,
  'keyId': keyId,
  'nonce': nonce,
  'ts': now.millisecondsSinceEpoch ~/ 1000,
});

Uint8List payPayload({
  required String txnId,
  required int amount,
  required String payee,
  required String nonce,
  required String deviceId,
  required String keyId,
  required DateTime now,
}) => _encode({
  'purpose': 'upi.pay',
  'txnId': txnId,
  'amount': amount,
  'payee': payee,
  'nonce': nonce,
  'deviceId': deviceId,
  'keyId': keyId,
  'ts': now.millisecondsSinceEpoch ~/ 1000,
});

/// What the old phone signs to let a new phone in. It names the new phone's
/// key, so the approval cannot be moved to a different device.
Uint8List approvePayload({
  required String approvalId,
  required String newKeyId,
  required String nonce,
  required String deviceId,
  required String keyId,
  required DateTime now,
}) => _encode({
  'purpose': 'device.approve',
  'approvalId': approvalId,
  'newKeyId': newKeyId,
  'nonce': nonce,
  'deviceId': deviceId,
  'keyId': keyId,
  'ts': now.millisecondsSinceEpoch ~/ 1000,
});

/// Plays the attacker for the demo: rewrites the payee after signing.
Uint8List rewritePayee(Uint8List signedPayload, String newPayee) {
  final fields = jsonDecode(utf8.decode(signedPayload)) as Map<String, dynamic>;
  fields['payee'] = newPayee;
  return _encode(fields);
}

Uint8List _encode(Map<String, dynamic> fields) =>
    Uint8List.fromList(utf8.encode(jsonEncode(fields)));
