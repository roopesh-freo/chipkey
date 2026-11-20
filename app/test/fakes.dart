import 'dart:typed_data';

import 'package:demo/backend.dart';
import 'package:demo/chip.dart';

class FakeChip implements KeyChip {
  FakeChip({this.id = 'd-test', this.name = 'Test Phone'});
  final String id;
  final String name;

  bool enrolled = false;
  bool fallBack = false;
  String biometrics = 'ready';
  int enrolCount = 0;
  ChipException? signError;

  /// Fails the next signature only, as a freshly invalidated key would.
  ChipException? signErrorOnce;
  final signed = <Uint8List>[];
  final prompts = <String>[];

  String get keyId => 'key-$id';

  @override
  Future<String> deviceId() async => id;

  @override
  Future<String> deviceName() async => name;

  @override
  Future<ChipStatus> status() async => ChipStatus(
    enrolled: enrolled,
    biometrics: biometrics,
    tier: enrolled ? 'tee' : null,
    keyId: enrolled ? keyId : null,
  );

  @override
  Future<Enrolment> enrol(Uint8List serverChallenge) async {
    enrolled = true;
    enrolCount++;
    return Enrolment(
      chain: [serverChallenge, Uint8List(2), Uint8List(3)],
      tier: 'tee',
      keyId: keyId,
      fellBackFromStrongBox: fallBack,
    );
  }

  @override
  Future<Uint8List> sign(
    Uint8List payload, {
    required String title,
    String subtitle = '',
  }) async {
    final error = signErrorOnce ?? signError;
    signErrorOnce = null;
    if (error != null) {
      if (error.code == 'KEY_INVALIDATED') enrolled = false;
      throw error;
    }
    signed.add(payload);
    prompts.add('$title|$subtitle');
    return Uint8List.fromList([7, 7, 7]);
  }

  @override
  Future<void> delete() async => enrolled = false;
}

class _Ask {
  _Ask(this.id, this.newDevice, this.askDevice, this.name);
  final String id;
  final String newDevice;
  final String askDevice;
  final String name;
  String status = 'pending';
}

/// The verifier's rules in miniature, shared by every phone in a test.
class FakeServer {
  /// The old app: an OTP is the whole login.
  bool legacy = false;
  bool fullLimits = true;
  int funds = 100000;

  /// Her phone forwards every SMS to the thief.
  bool forwarding = true;

  /// Phones whose key the account trusts.
  final bound = <String>{};
  final asks = <_Ask>[];
  final inbox = <SmsMessage>[];
  final forwardedIds = <int>{};
  final recoveries = <String>[];

  /// The dashboard reset the verifier: every token is dead.
  bool sessionsWiped = false;
  String? code;
  int _codes = 0;

  void sendOtp() {
    _codes++;
    code = '${_codes * 111111}';
    final id = inbox.length + 1;
    inbox.add(SmsMessage(id, '$code is your OTP to log in.', '23:52'));
    if (forwarding) forwardedIds.add(id);
  }
}

class FakeBackend implements BackendApi {
  FakeBackend([FakeServer? server]) : server = server ?? FakeServer();
  final FakeServer server;

  bool token = false;
  Object? failNext;

  /// Thrown by confirm, after the payload has been recorded.
  Object? failConfirm;
  final confirmed = <Uint8List>[];
  final started = <int>[];
  final pending = <String, int>{};
  final logins = <Uint8List>[];
  final approvals = <Uint8List>[];

  int get funds => server.funds;

  void _maybeFail() {
    final f = failNext;
    failNext = null;
    if (f != null) throw f;
  }

  @override
  bool get hasToken => token;

  @override
  void dropToken() => token = false;

  @override
  Future<LoginStep> loginStart(
    String phone,
    String deviceId, {
    required bool hasKey,
  }) async {
    _maybeFail();
    if (server.legacy) {
      server.sendOtp();
      return const LoginStep('otp');
    }
    if (server.bound.contains(deviceId) && hasKey) {
      return const LoginStep('sign', nonce: 'nonce-1');
    }
    if (server.bound.isEmpty || server.bound.contains(deviceId)) {
      return LoginStep('enrol', challenge: Uint8List.fromList([1, 2, 3]));
    }
    server.sendOtp();
    return const LoginStep('otp');
  }

  @override
  Future<LoginStep> loginOtp(
    String phone,
    String deviceId,
    String deviceName,
    String otp,
  ) async {
    _maybeFail();
    if (otp != server.code) {
      throw const ApiException(
        401,
        'otp_invalid',
        'that code is wrong or has expired',
        {},
      );
    }
    server.code = null;
    if (server.legacy) {
      token = true;
      return const LoginStep('token', tokenTtl: Duration(minutes: 15));
    }
    return LoginStep(
      'enrol',
      newDevice: true,
      challenge: Uint8List.fromList([1, 2, 3]),
    );
  }

  @override
  Future<EnrolVerdict> enrol(
    String deviceId,
    List<Uint8List> chain, {
    String? phone,
    String? deviceName,
  }) async {
    token = false;
    String? approvalId;
    if (server.bound.isEmpty || server.bound.contains(deviceId)) {
      server.bound.add(deviceId);
    } else {
      final ask = _Ask(
        'A-${server.asks.length + 1}',
        deviceId,
        server.bound.first,
        deviceName ?? deviceId,
      );
      server.asks.add(ask);
      approvalId = ask.id;
    }
    return EnrolVerdict(
      keyId: 'key-$deviceId',
      tier: server.fullLimits ? 'tee' : 'software',
      integrity: server.fullLimits,
      fullLimits: server.fullLimits,
      threshold: 2000,
      reducedLimit: 500,
      approvalId: approvalId,
    );
  }

  @override
  Future<Duration> login(
    String deviceId,
    Uint8List payload,
    Uint8List signature,
  ) async {
    if (!server.bound.contains(deviceId)) {
      throw const ApiException(403, 'not_bound', 'this phone is not bound', {});
    }
    logins.add(payload);
    token = true;
    return const Duration(minutes: 15);
  }

  @override
  Future<String> approvalStatus(String id) async =>
      server.asks.firstWhere((a) => a.id == id).status;

  @override
  Future<List<ApprovalRequest>> pendingApprovals(String deviceId) async => [
    for (final a in server.asks)
      if (a.askDevice == deviceId && a.status == 'pending')
        ApprovalRequest(
          id: a.id,
          deviceName: a.name,
          tier: 'tee',
          newKeyId: 'key-${a.newDevice}',
          nonce: 'nonce-${a.id}',
          requestedAt: '23:54',
        ),
  ];

  @override
  Future<void> decideApproval(
    String id,
    String deviceId, {
    required bool approve,
    Uint8List? payload,
    Uint8List? signature,
  }) async {
    final ask = server.asks.firstWhere((a) => a.id == id);
    if (!approve) {
      ask.status = 'blocked';
      return;
    }
    if (payload == null || signature == null) {
      throw const ApiException(
        401,
        'bad_signature',
        'approval needs a signature',
        {},
      );
    }
    approvals.add(payload);
    ask.status = 'approved';
    server.bound.add(ask.newDevice);
  }

  @override
  Future<List<SmsMessage>> sms(
    String phone, {
    required bool forwarded,
    int after = 0,
  }) async => [
    for (final m in server.inbox)
      if (m.id > after && (!forwarded || server.forwardedIds.contains(m.id))) m,
  ];

  @override
  Future<void> startRecovery(String phone, String deviceId) async {
    _maybeFail();
    server.recoveries.add(deviceId);
  }

  @override
  Future<int> balance() async {
    if (!token || server.sessionsWiped) {
      throw const ApiException(401, 'token_invalid', 'no session', {});
    }
    return server.funds;
  }

  @override
  Future<PaymentIntent> startPayment(int amount, String payee) async {
    _maybeFail();
    started.add(amount);
    final chip = amount >= 2000 && !server.legacy;
    pending[chip ? 'T-1' : 'T-0'] = amount;
    return chip
        ? const PaymentIntent('T-1', 'nonce-2')
        : const PaymentIntent('T-0');
  }

  @override
  Future<int> confirm(
    String txnId, {
    Uint8List? payload,
    Uint8List? signature,
  }) async {
    _maybeFail();
    if (payload != null) confirmed.add(payload);
    final refused = failConfirm;
    if (refused != null) throw refused;
    return server.funds -= pending.remove(txnId)!;
  }
}

/// A cancelled biometric prompt.
class FakeCancel extends ChipException {
  const FakeCancel() : super('CANCELLED', 'user cancelled');
}
