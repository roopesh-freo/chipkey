import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

class ApiException implements Exception {
  const ApiException(this.status, this.code, this.message, this.body);
  final int status;
  final String code;
  final String message;
  final Map<String, dynamic> body;

  @override
  String toString() => 'HTTP $status $code: $message';
}

class EnrolVerdict {
  const EnrolVerdict({
    required this.keyId,
    required this.tier,
    required this.integrity,
    required this.fullLimits,
    required this.threshold,
    required this.reducedLimit,
    this.approvalId,
  });

  final String keyId;
  final String tier;
  final bool integrity;
  final bool fullLimits;
  final int threshold;
  final int reducedLimit;

  /// Set when the account is already bound to another phone: this phone is
  /// not in yet, and that phone has been asked to approve it.
  final String? approvalId;
}

/// What the backend wants from this phone next.
class LoginStep {
  const LoginStep(
    this.next, {
    this.nonce,
    this.challenge,
    this.newDevice = false,
    this.tokenTtl,
  });

  /// "otp", "enrol", "sign" or "token".
  final String next;
  final String? nonce;
  final Uint8List? challenge;
  final bool newDevice;
  final Duration? tokenTtl;
}

/// A new phone asking this phone to let it in.
class ApprovalRequest {
  const ApprovalRequest({
    required this.id,
    required this.deviceName,
    required this.tier,
    required this.newKeyId,
    required this.nonce,
    required this.requestedAt,
  });
  final String id;
  final String deviceName;
  final String tier;
  final String newKeyId;
  final String nonce;
  final String requestedAt;
}

class SmsMessage {
  const SmsMessage(this.id, this.text, this.at);
  final int id;
  final String text;
  final String at;

  /// The six-digit code at the start of an OTP message, if there is one.
  String? get code => RegExp(r'^\d{6}').stringMatch(text);
}

/// A payment the backend has accepted but not yet paid. When [nonce] is
/// set, the backend wants the transaction signed by the chip first.
class PaymentIntent {
  const PaymentIntent(this.txnId, [this.nonce]);
  final String txnId;
  final String? nonce;
  bool get needsChip => nonce != null;
}

abstract class BackendApi {
  bool get hasToken;
  void dropToken();

  /// Opens a login for a phone number. The answer says what to do next.
  Future<LoginStep> loginStart(
    String phone,
    String deviceId, {
    required bool hasKey,
  });

  /// Submits an OTP. In the old app that is the whole login.
  Future<LoginStep> loginOtp(
    String phone,
    String deviceId,
    String deviceName,
    String otp,
  );

  Future<EnrolVerdict> enrol(
    String deviceId,
    List<Uint8List> chain, {
    String? phone,
    String? deviceName,
  });

  /// Trades a signed nonce for an X-Token, which is kept for later calls.
  Future<Duration> login(
    String deviceId,
    Uint8List payload,
    Uint8List signature,
  );

  /// "pending", "approved", "blocked" or "expired".
  Future<String> approvalStatus(String id);
  Future<List<ApprovalRequest>> pendingApprovals(String deviceId);
  Future<void> decideApproval(
    String id,
    String deviceId, {
    required bool approve,
    Uint8List? payload,
    Uint8List? signature,
  });

  /// The SMS sent to a number. [forwarded] is the copy the thief receives.
  Future<List<SmsMessage>> sms(
    String phone, {
    required bool forwarded,
    int after = 0,
  });
  Future<void> startRecovery(String phone, String deviceId);
  Future<int> balance();

  /// Opens a payment. Nothing is paid until [confirm].
  Future<PaymentIntent> startPayment(int amount, String payee);

  /// Completes a payment and returns the new balance. A payment that needs
  /// the chip must carry the signed payload.
  Future<int> confirm(String txnId, {Uint8List? payload, Uint8List? signature});
}

/// With "adb reverse tcp:7447 tcp:7447" the phone reaches the laptop here,
/// so the demo does not depend on conference Wi-Fi.
const defaultBackendUrl = 'http://127.0.0.1:7447';

class HttpBackend implements BackendApi {
  HttpBackend(this._baseUrl, {this.onBaseUrlChanged});

  String _baseUrl;

  /// Called when the address is changed from the demo controls, so it can
  /// be saved for the next launch.
  final void Function(String url)? onBaseUrlChanged;

  String get baseUrl => _baseUrl;

  /// Points the app at another verifier. The session belongs to the old
  /// one, so it is dropped.
  void setBaseUrl(String url) {
    final cleaned = url.trim().replaceAll(RegExp(r'/+$'), '');
    if (cleaned.isEmpty || cleaned == _baseUrl) return;
    _baseUrl = cleaned;
    _token = null;
    onBaseUrlChanged?.call(cleaned);
  }

  String? _token;
  final _client = HttpClient()..connectionTimeout = const Duration(seconds: 4);

  @override
  bool get hasToken => _token != null;

  @override
  void dropToken() => _token = null;

  Future<Map<String, dynamic>> _call(
    String method,
    String path, [
    Map<String, dynamic>? body,
  ]) async {
    final request = await _client.openUrl(method, Uri.parse('$baseUrl$path'));
    request.headers.contentType = ContentType.json;
    if (_token != null) request.headers.set('X-Token', _token!);
    if (body != null) request.write(jsonEncode(body));
    final response = await request.close().timeout(const Duration(seconds: 8));
    final text = await response.transform(utf8.decoder).join();
    final decoded = text.isEmpty
        ? <String, dynamic>{}
        : jsonDecode(text) as Map<String, dynamic>;
    if (response.statusCode ~/ 100 != 2) {
      throw ApiException(
        response.statusCode,
        decoded['error'] as String? ?? 'http_${response.statusCode}',
        decoded['message'] as String? ?? '',
        decoded,
      );
    }
    return decoded;
  }

  LoginStep _step(Map<String, dynamic> r) {
    final token = r['token'] as String?;
    if (token != null) _token = token;
    final challenge = r['challenge'] as String?;
    final ttl = r['expiresInSeconds'] as int?;
    return LoginStep(
      r['next'] as String,
      nonce: r['nonce'] as String?,
      challenge: challenge == null ? null : base64Decode(challenge),
      newDevice: r['newDevice'] == true,
      tokenTtl: ttl == null ? null : Duration(seconds: ttl),
    );
  }

  @override
  Future<LoginStep> loginStart(
    String phone,
    String deviceId, {
    required bool hasKey,
  }) async => _step(
    await _call('POST', '/v1/login/start', {
      'phone': phone,
      'deviceId': deviceId,
      'hasKey': hasKey,
    }),
  );

  @override
  Future<LoginStep> loginOtp(
    String phone,
    String deviceId,
    String deviceName,
    String otp,
  ) async => _step(
    await _call('POST', '/v1/login/otp', {
      'phone': phone,
      'deviceId': deviceId,
      'deviceName': deviceName,
      'otp': otp,
    }),
  );

  @override
  Future<EnrolVerdict> enrol(
    String deviceId,
    List<Uint8List> chain, {
    String? phone,
    String? deviceName,
  }) async {
    final r = await _call('POST', '/v1/enrol', {
      'deviceId': deviceId,
      'chain': chain.map(base64Encode).toList(),
      'phone': ?phone,
      'deviceName': ?deviceName,
    });
    _token = null;
    return EnrolVerdict(
      keyId: r['keyId'] as String,
      tier: r['tier'] as String,
      integrity: r['integrity'] as bool,
      fullLimits: r['fullLimits'] as bool,
      threshold: r['threshold'] as int,
      reducedLimit: r['reducedLimit'] as int,
      approvalId: r['approvalId'] as String?,
    );
  }

  @override
  Future<Duration> login(
    String deviceId,
    Uint8List payload,
    Uint8List signature,
  ) async {
    final r = await _call('POST', '/v1/session', {
      'deviceId': deviceId,
      'payload': base64Encode(payload),
      'signature': base64Encode(signature),
    });
    _token = r['token'] as String;
    return Duration(seconds: r['expiresInSeconds'] as int);
  }

  @override
  Future<String> approvalStatus(String id) async =>
      (await _call('GET', '/v1/approvals/$id'))['status'] as String;

  @override
  Future<List<ApprovalRequest>> pendingApprovals(String deviceId) async {
    final r = await _call(
      'GET',
      '/v1/approvals/pending?deviceId=${Uri.encodeQueryComponent(deviceId)}',
    );
    return [
      for (final a in (r['approvals'] as List).cast<Map<String, dynamic>>())
        ApprovalRequest(
          id: a['id'] as String,
          deviceName: a['deviceName'] as String,
          tier: a['tier'] as String,
          newKeyId: a['newKeyId'] as String,
          nonce: a['nonce'] as String,
          requestedAt: a['requestedAt'] as String,
        ),
    ];
  }

  @override
  Future<void> decideApproval(
    String id,
    String deviceId, {
    required bool approve,
    Uint8List? payload,
    Uint8List? signature,
  }) => _call('POST', '/v1/approvals/$id/decision', {
    'deviceId': deviceId,
    'approve': approve,
    if (payload != null) 'payload': base64Encode(payload),
    if (signature != null) 'signature': base64Encode(signature),
  });

  @override
  Future<List<SmsMessage>> sms(
    String phone, {
    required bool forwarded,
    int after = 0,
  }) async {
    final view = forwarded ? '&view=forwarded' : '';
    final r = await _call(
      'GET',
      '/v1/sms?phone=${Uri.encodeQueryComponent(phone)}&after=$after$view',
    );
    return [
      for (final m in (r['messages'] as List).cast<Map<String, dynamic>>())
        SmsMessage(m['id'] as int, m['text'] as String, m['at'] as String),
    ];
  }

  @override
  Future<void> startRecovery(String phone, String deviceId) => _call(
    'POST',
    '/v1/recovery/start',
    {'phone': phone, 'deviceId': deviceId},
  );

  @override
  Future<int> balance() async =>
      (await _call('GET', '/v1/balance'))['balance'] as int;

  @override
  Future<PaymentIntent> startPayment(int amount, String payee) async {
    final r = await _call('POST', '/v1/pay', {
      'amount': amount,
      'payee': payee,
    });
    return PaymentIntent(
      r['txnId'] as String,
      r['stepUp'] == true ? r['nonce'] as String : null,
    );
  }

  @override
  Future<int> confirm(
    String txnId, {
    Uint8List? payload,
    Uint8List? signature,
  }) async {
    final r = await _call('POST', '/v1/pay/confirm', {
      'txnId': txnId,
      'payload': payload == null ? '' : base64Encode(payload),
      'signature': signature == null ? '' : base64Encode(signature),
    });
    return r['balance'] as int;
  }
}
