// Drives the real Go verifier from the real Dart client and controller.
// Only the chip is faked, by ../server/cmd/fakechip, because a test process
// has no Android Keystore. Skipped when Go is not installed.
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:demo/backend.dart';
import 'package:demo/chip.dart';
import 'package:demo/controller.dart';
import 'package:flutter_test/flutter_test.dart';

class ProcessChip implements KeyChip {
  ProcessChip(this.binary, this.caDir, this.stateFile, this.id, this.name);
  final String binary;
  final String caDir;
  final String stateFile;
  final String id;
  final String name;
  String? _keyId;

  Future<String> _run(List<String> args) async {
    final r = await Process.run(binary, [
      '-ca',
      caDir,
      '-state',
      stateFile,
      ...args,
    ]);
    if (r.exitCode != 0) throw ChipException('FAKECHIP', r.stderr as String);
    return (r.stdout as String).trim();
  }

  @override
  Future<String> deviceId() async => 'd-$id-${pid.toRadixString(16)}';

  @override
  Future<String> deviceName() async => name;

  @override
  Future<ChipStatus> status() async => ChipStatus(
    enrolled: _keyId != null,
    biometrics: 'ready',
    tier: _keyId == null ? null : 'strongbox',
    keyId: _keyId,
  );

  @override
  Future<Enrolment> enrol(Uint8List serverChallenge) async {
    final out =
        jsonDecode(await _run(['enrol', base64Encode(serverChallenge)]))
            as Map<String, dynamic>;
    _keyId = out['keyId'] as String;
    return Enrolment(
      chain: [for (final c in out['chain'] as List) base64Decode(c as String)],
      tier: out['tier'] as String,
      keyId: _keyId!,
      fellBackFromStrongBox: false,
    );
  }

  @override
  Future<Uint8List> sign(
    Uint8List payload, {
    required String title,
    String subtitle = '',
  }) async => base64Decode(await _run(['sign', base64Encode(payload)]));

  @override
  Future<void> delete() async => _keyId = null;
}

void main() {
  final serverDir = Directory('../server');
  final hasGo = () {
    try {
      return serverDir.existsSync() &&
          Process.runSync('go', ['version']).exitCode == 0;
    } on ProcessException {
      return false;
    }
  }();

  late Directory tmp;
  late Process verifier;
  late String baseUrl;
  final serverLog = StringBuffer();

  setUpAll(() async {
    if (!hasGo) return;
    HttpOverrides.global = null;
    tmp = await Directory.systemTemp.createTemp('chipkey-e2e');
    for (final cmd in ['verifier', 'fakechip']) {
      final r = await Process.run('go', [
        'build',
        '-o',
        '${tmp.path}/$cmd',
        './cmd/$cmd',
      ], workingDirectory: serverDir.path);
      expect(r.exitCode, 0, reason: r.stderr as String);
    }
    final init = await Process.run('${tmp.path}/fakechip', [
      '-ca',
      '${tmp.path}/ca',
      'init',
    ]);
    expect(init.exitCode, 0, reason: init.stderr as String);

    final probe = await ServerSocket.bind(InternetAddress.loopbackIPv4, 0);
    final port = probe.port;
    await probe.close();
    baseUrl = 'http://127.0.0.1:$port';
    verifier = await Process.start('${tmp.path}/verifier', [
      '-addr',
      '127.0.0.1:$port',
      '-extra-root',
      '${tmp.path}/ca/root.pem',
      '-approval-ttl',
      '2s',
      // The stage controls are driven without a dashboard login here.
      '-dash-pass',
      '',
      '-no-color',
    ]);
    verifier.stdout.transform(utf8.decoder).listen(serverLog.write);
    verifier.stderr.transform(utf8.decoder).listen(serverLog.write);

    final client = HttpClient();
    for (var i = 0; i < 100; i++) {
      try {
        final r = await (await client.getUrl(
          Uri.parse('$baseUrl/healthz'),
        )).close();
        await r.drain<void>();
        if (r.statusCode == 200) break;
      } on SocketException {
        await Future<void>.delayed(const Duration(milliseconds: 50));
      }
    }
    client.close();
  });

  tearDownAll(() async {
    if (!hasGo) return;
    verifier.kill();
    await verifier.exitCode;
    await tmp.delete(recursive: true);
  });

  /// The dashboard's stage keys: switch nights and forget everything.
  Future<void> rewind(String mode) async {
    final client = HttpClient();
    final req = await client.postUrl(Uri.parse('$baseUrl/v1/demo'));
    req.headers.contentType = ContentType.json;
    req.write(jsonEncode({'mode': mode, 'reset': true}));
    final res = await req.close();
    await res.drain<void>();
    client.close();
    expect(res.statusCode, 200);
  }

  DemoController phone(String id, String name, Role role) => DemoController(
    chip: ProcessChip(
      '${tmp.path}/fakechip',
      '${tmp.path}/ca',
      '${tmp.path}/$id.key',
      id,
      name,
    ),
    backend: HttpBackend(baseUrl),
    role: role,
    pollEvery: null,
  );

  test(
    'one night, played twice, against the real verifier',
    () async {
      const number = '98765 43210';
      final meera = phone('meera', 'OnePlus IV2201', Role.owner);
      final thief = phone('thief', 'Google Pixel 9', Role.thief);
      await meera.init();
      await thief.init();

      /// The thief types her number and uses the OTP her phone forwards.
      Future<void> thiefUsesTheOtp() async {
        await thief.tick();
        await thief.startLogin(number);
        expect(thief.stage, LoginStage.otp, reason: thief.error);
        await thief.tick();
        expect(thief.banner?.code, isNotNull, reason: 'no forwarded SMS');
        thief.useBannerCode();
        await thief.submitOtp(thief.otpDraft);
      }

      // ---- the night as it happened: the old app ----
      await rewind('legacy');
      await thiefUsesTheOtp();
      expect(thief.signedIn, isTrue, reason: thief.error);
      expect(thief.otpOnlySession, isTrue);
      expect(thief.status.enrolled, isFalse);
      var r = await thief.pay(25000, 'evil@upi');
      expect(r.ok, isTrue, reason: r.message);
      expect(r.steppedUp, isFalse);
      expect(thief.balance, 75000);
      thief.logout();

      // ---- rewind: the same night with the chip ----
      await rewind('chip');
      await meera.startLogin(number);
      expect(meera.signedIn, isTrue, reason: meera.error);
      expect(meera.verdict?.tier, 'strongbox');
      expect(meera.verdict?.fullLimits, isTrue);
      expect(
        meera.verdict?.keyId,
        meera.status.keyId,
        reason: 'phone and backend must derive the same key id',
      );
      expect(meera.balance, 100000);

      // He has the OTP and a genuine chip, and is still outside.
      await thiefUsesTheOtp();
      expect(thief.stage, LoginStage.waiting, reason: thief.error);
      expect(thief.signedIn, isFalse);

      // Her phone is asked. She is asleep: after two seconds it dies.
      await meera.tick();
      expect(meera.incoming?.deviceName, 'Google Pixel 9');
      await Future<void>.delayed(const Duration(milliseconds: 2300));
      await thief.tick();
      expect(thief.stage, LoginStage.refused);
      expect(thief.refusedReason, contains('did not approve'));
      await meera.tick();
      expect(meera.incoming, isNull);

      // "I lost my phone" is a slow door, not an OTP.
      await thief.lostMyPhone();
      expect(thief.stage, LoginStage.recovery, reason: thief.error);
      expect(thief.signedIn, isFalse);
      thief.backToStart();

      // He tries again while she is awake. One tap blocks him.
      await thiefUsesTheOtp();
      expect(thief.stage, LoginStage.waiting, reason: thief.error);
      await meera.tick();
      await meera.blockIncoming();
      expect(meera.error, isNull);
      await thief.tick();
      expect(thief.stage, LoginStage.refused);
      expect(thief.refusedReason, contains('blocked'));
      thief.backToStart();

      // Her money is where it was, and her phone works as before.
      r = await meera.pay(500, 'chai@upi');
      expect(r.ok, isTrue, reason: r.message);
      expect(r.steppedUp, isFalse);
      r = await meera.pay(25000, 'shop@upi');
      expect(r.ok, isTrue, reason: r.message);
      expect(r.steppedUp, isTrue);
      expect(meera.balance, 74500);

      meera.setTamper(true);
      r = await meera.pay(25000, 'shop@upi');
      expect(r.blocked, isTrue, reason: r.message);
      expect(meera.balance, 74500);
      meera.setTamper(false);

      // A second login on the same key works without binding again.
      meera.logout();
      await meera.startLogin(number);
      expect(meera.signedIn, isTrue, reason: meera.error);

      // The honest version of a new phone: the same steps, and this time
      // the old phone signs for it with a fingerprint.
      await thiefUsesTheOtp();
      expect(thief.stage, LoginStage.waiting, reason: thief.error);
      await meera.tick();
      await meera.approveIncoming();
      expect(meera.error, isNull);
      await thief.tick();
      expect(thief.signedIn, isTrue, reason: thief.error);
      expect(thief.otpOnlySession, isFalse);
      expect(thief.balance, 74500);

      await Future<void>.delayed(const Duration(milliseconds: 100));
      final log = serverLog.toString();
      expect(log, contains('tier=STRONGBOX'));
      expect(log, contains('signature INVALID'));
      expect(log, contains('₹25,000 to shop@upi. Paid.'));
      expect(log, contains('was NOT let in'));
      expect(log, contains('with a fingerprint. The new phone is bound'));
    },
    timeout: const Timeout(Duration(minutes: 2)),
    skip: hasGo ? false : 'Go toolchain not found',
  );
}
