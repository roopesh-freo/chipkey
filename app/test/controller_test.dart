import 'dart:convert';
import 'dart:io';

import 'package:demo/backend.dart';
import 'package:demo/chip.dart';
import 'package:demo/controller.dart';
import 'package:demo/payloads.dart';
import 'package:flutter_test/flutter_test.dart';

import 'fakes.dart';

Map<String, dynamic> fields(List<int> payload) =>
    jsonDecode(utf8.decode(payload)) as Map<String, dynamic>;

void main() {
  final fixedNow = DateTime.fromMillisecondsSinceEpoch(1795257600 * 1000);
  late FakeChip chip;
  late FakeBackend backend;
  late DemoController c;

  setUp(() async {
    chip = FakeChip();
    backend = FakeBackend();
    c = DemoController(
      chip: chip,
      backend: backend,
      clock: () => fixedNow,
      pollEvery: null,
    );
    await c.init();
  });

  Iterable<String> said() => c.log.map((l) => l.text);

  test('payloads carry a purpose and bind device, key, nonce and time', () {
    final login = fields(
      loginPayload(deviceId: 'd', keyId: 'k', nonce: 'n', now: fixedNow),
    );
    expect(login, {
      'purpose': 'session.login',
      'deviceId': 'd',
      'keyId': 'k',
      'nonce': 'n',
      'ts': 1795257600,
    });

    final pay = fields(
      payPayload(
        txnId: 'T-9',
        amount: 25000,
        payee: 'shop@upi',
        nonce: 'n',
        deviceId: 'd',
        keyId: 'k',
        now: fixedNow,
      ),
    );
    expect(pay['purpose'], 'upi.pay');
    expect(pay['amount'], 25000);
    expect(pay['payee'], 'shop@upi');
    expect(pay['txnId'], 'T-9');
  });

  test('rewritePayee changes the payee and nothing else', () {
    final signed = payPayload(
      txnId: 'T-9',
      amount: 25000,
      payee: 'shop@upi',
      nonce: 'n',
      deviceId: 'd',
      keyId: 'k',
      now: fixedNow,
    );
    final forged = rewritePayee(signed, 'evil@upi');
    expect(forged, isNot(signed));
    expect(fields(forged), {...fields(signed), 'payee': 'evil@upi'});
  });

  test('rupees uses Indian grouping', () {
    expect(rupees(500), '₹500');
    expect(rupees(2000), '₹2,000');
    expect(rupees(25000), '₹25,000');
    expect(rupees(100000), '₹1,00,000');
    expect(rupees(12345678), '₹1,23,45,678');
  });

  /// Logs in on the default number and says whether a session resulted.
  Future<bool> login([DemoController? who]) async {
    final phone = who ?? c;
    await phone.startLogin('98765 43210');
    return phone.signedIn;
  }

  group('login', () {
    test(
      'first login binds the device, then signs a login challenge',
      () async {
        expect(await login(), isTrue);
        expect(chip.enrolCount, 1);
        expect(c.status.enrolled, isTrue);
        expect(c.verdict!.fullLimits, isTrue);
        expect(c.signedIn, isTrue);
        expect(c.otpOnlySession, isFalse);
        expect(c.balance, 100000);
        expect(c.error, isNull);
        expect(c.busy, isFalse);
        expect(
          fields(chip.signed.single),
          containsPair('purpose', 'session.login'),
        );
        expect(fields(chip.signed.single), containsPair('nonce', 'nonce-1'));
        expect(backend.logins.single, chip.signed.single);
        expect(chip.prompts.single, 'Log in|Unlock your device key');
        expect(backend.server.inbox, isEmpty, reason: 'no OTP was needed');
      },
    );

    test('a later login reuses the key', () async {
      await login();
      c.logout();
      expect(c.signedIn, isFalse);
      expect(await login(), isTrue);
      expect(chip.enrolCount, 1);
      expect(chip.signed, hasLength(2));
    });

    test('a number that is not ten digits never reaches the backend', () async {
      await c.startLogin('98765');
      expect(c.error, contains('ten-digit'));
      expect(chip.enrolCount, 0);
    });

    test('a StrongBox fallback and reduced limits are reported', () async {
      chip.fallBack = true;
      backend.server.fullLimits = false;
      expect(await login(), isTrue);
      expect(said(), contains(contains('StrongBox is not available')));
      expect(said(), contains(contains('reduced limits of ₹500')));
    });

    test('a backend that forgot the device gets a fresh binding', () async {
      await login();
      c.logout();
      backend.server.bound.clear();
      expect(await login(), isTrue);
      expect(chip.enrolCount, 2);
      expect(c.signedIn, isTrue);
    });

    test('a key killed by a new fingerprint is replaced', () async {
      await login();
      c.logout();
      chip.signErrorOnce = const ChipException('KEY_INVALIDATED', 'gone');
      expect(await login(), isTrue);
      expect(chip.enrolCount, 2);
      expect(said(), contains(contains('invalidated by a biometric change')));
      expect(c.signedIn, isTrue);
    });

    test('a cancelled fingerprint prompt leaves the user logged out', () async {
      chip.signError = const ChipException('CANCELLED', 'user cancelled');
      expect(await login(), isFalse);
      expect(c.signedIn, isFalse);
      expect(c.error, contains('cancelled'));
      expect(c.busy, isFalse);
    });

    test('a phone without a strong biometric is told what to do', () async {
      chip.biometrics = 'none_enrolled';
      expect(await login(), isFalse);
      expect(c.error, contains('fingerprint'));
      expect(chip.enrolCount, 0);
    });

    test('an unreachable backend points at adb reverse', () async {
      backend.failNext = const SocketException('refused');
      expect(await login(), isFalse);
      expect(c.error, contains('adb reverse'));
    });
  });

  group('the night as it happened', () {
    late FakeChip hisChip;
    late FakeBackend hisBackend;
    late DemoController thief;

    setUp(() async {
      backend.server.legacy = true;
      hisChip = FakeChip(id: 'd-thief', name: 'Google Pixel 9');
      hisBackend = FakeBackend(backend.server);
      thief = DemoController(
        chip: hisChip,
        backend: hisBackend,
        clock: () => fixedNow,
        role: Role.thief,
        pollEvery: null,
      );
      await thief.init();
      await thief.tick();
    });

    test('the forwarded OTP is the whole login, and the money moves', () async {
      await thief.startLogin('9876543210');
      expect(thief.stage, LoginStage.otp);
      expect(thief.signedIn, isFalse);

      // Her phone forwards the SMS. It shows up on his.
      await thief.tick();
      expect(thief.banner!.code, backend.server.code);
      thief.useBannerCode();
      expect(thief.otpDraft, backend.server.code);
      expect(thief.banner, isNull);

      await thief.submitOtp(thief.otpDraft);
      expect(thief.signedIn, isTrue);
      expect(thief.otpOnlySession, isTrue);
      expect(hisChip.enrolCount, 0, reason: 'no key was ever involved');

      final r = await thief.pay(25000, 'evil@upi');
      expect(r.ok, isTrue);
      expect(r.steppedUp, isFalse);
      expect(hisChip.signed, isEmpty);
      expect(backend.server.funds, 75000);
    });

    test('a wrong OTP is refused', () async {
      await thief.startLogin('9876543210');
      await thief.submitOtp('000000');
      expect(thief.signedIn, isFalse);
      expect(thief.stage, LoginStage.otp);
      expect(thief.error, contains('wrong or has expired'));
    });

    test('without forwarding the thief sees no SMS', () async {
      backend.server.forwarding = false;
      await thief.startLogin('9876543210');
      await thief.tick();
      expect(thief.banner, isNull);
      // Her own phone still gets it.
      await c.tick();
      await thief.startLogin('9876543210');
      await c.tick();
      expect(c.banner, isNotNull);
    });
  });

  group('the same night with the chip', () {
    late FakeChip hisChip;
    late FakeBackend hisBackend;
    late DemoController thief;

    setUp(() async {
      // Meera bound her phone weeks ago.
      await login();
      chip.signed.clear();
      chip.prompts.clear();
      hisChip = FakeChip(id: 'd-thief', name: 'Google Pixel 9');
      hisBackend = FakeBackend(backend.server);
      thief = DemoController(
        chip: hisChip,
        backend: hisBackend,
        clock: () => fixedNow,
        role: Role.thief,
        pollEvery: null,
      );
      await thief.init();
      await thief.tick();
    });

    /// The thief gets as far as the forwarded OTP takes him.
    Future<void> thiefTries() async {
      await thief.startLogin('9876543210');
      expect(thief.stage, LoginStage.otp);
      await thief.tick();
      thief.useBannerCode();
      await thief.submitOtp(thief.otpDraft);
    }

    test(
      'the OTP only proves the number: his genuine phone has to wait',
      () async {
        await thiefTries();
        expect(hisChip.enrolCount, 1, reason: 'his chip is real, and attests');
        expect(thief.stage, LoginStage.waiting);
        expect(thief.signedIn, isFalse);
        expect(hisChip.signed, isEmpty);
        expect(said().join(), isNot(contains('Pixel')));
        expect(
          thief.log.map((l) => l.text),
          contains(contains('bound to another phone')),
        );
      },
    );

    test('her phone is asked, blocks him, and he is told no', () async {
      await thiefTries();
      await c.tick();
      expect(c.incoming!.deviceName, 'Google Pixel 9');
      await c.blockIncoming();
      expect(c.incoming, isNull);
      expect(chip.signed, isEmpty, reason: 'saying no needs no fingerprint');

      await thief.tick();
      expect(thief.stage, LoginStage.refused);
      expect(thief.refusedReason, contains('blocked'));
      expect(thief.signedIn, isFalse);
    });

    test(
      'she sleeps through it, the request dies, and he is told no',
      () async {
        await thiefTries();
        backend.server.asks.single.status = 'expired';
        await thief.tick();
        expect(thief.stage, LoginStage.refused);
        expect(thief.refusedReason, contains('did not approve'));
      },
    );

    test('"I lost my phone" is not an OTP', () async {
      await thiefTries();
      await thief.lostMyPhone();
      expect(thief.stage, LoginStage.recovery);
      expect(thief.signedIn, isFalse);
      expect(backend.server.recoveries, ['d-thief']);
      thief.backToStart();
      expect(thief.stage, LoginStage.idle);
    });

    test('her own new phone gets in on the old phone\'s fingerprint', () async {
      await thiefTries(); // the same steps, taken by her this time
      await c.tick();
      await c.approveIncoming();
      expect(chip.prompts.single, 'Approve new device|Google Pixel 9');
      final signed = fields(chip.signed.single);
      expect(signed, containsPair('purpose', 'device.approve'));
      expect(signed, containsPair('approvalId', 'A-1'));
      expect(signed, containsPair('newKeyId', 'key-d-thief'));
      expect(signed, containsPair('nonce', 'nonce-A-1'));
      expect(backend.approvals.single, chip.signed.single);

      await thief.tick();
      expect(thief.signedIn, isTrue);
      expect(thief.stage, LoginStage.idle);
      expect(hisChip.prompts.single, 'Log in|Unlock your device key');
    });

    test('a cancelled fingerprint approves nothing', () async {
      await thiefTries();
      await c.tick();
      chip.signError = const FakeCancel();
      await c.approveIncoming();
      expect(c.incoming, isNotNull);
      expect(c.error, contains('cancelled'));
      await thief.tick();
      expect(thief.stage, LoginStage.waiting);
    });
  });

  group('after the dashboard rewinds', () {
    test('the balance follows the verifier', () async {
      await login();
      await c.pay(500, 'chai@upi');
      expect(c.balance, 99500);
      backend.server.funds = 100000;
      await c.tick();
      expect(c.balance, 100000);
      expect(c.signedIn, isTrue);
    });

    test('a wiped session falls back to the login screen', () async {
      await login();
      backend.server.sessionsWiped = true;
      await c.tick();
      expect(c.signedIn, isFalse);
      expect(c.balance, isNull);
    });
  });

  group('sms banner', () {
    test('old messages are history; a rewound verifier is news', () async {
      backend.server.sendOtp();
      await c.tick();
      expect(c.banner, isNull, reason: 'it arrived before the app opened');
      backend.server.sendOtp();
      await c.tick();
      expect(c.banner!.id, 2);
      c.dismissBanner();

      // The dashboard's rewind key empties the verifier.
      backend.server.inbox.clear();
      await c.tick();
      backend.server.sendOtp();
      await c.tick();
      expect(c.banner!.id, 1);
    });

    test('a banner outside the OTP screen fills nothing', () async {
      await c.tick();
      backend.server.sendOtp();
      await c.tick();
      c.useBannerCode();
      expect(c.otpDraft, isEmpty);
      expect(c.banner, isNull);
    });

    test('switching role is reported and starts a fresh inbox', () async {
      final roles = <Role>[];
      final phone = DemoController(
        chip: chip,
        backend: backend,
        pollEvery: null,
        onRoleChanged: roles.add,
      );
      await phone.init();
      phone.setRole(Role.thief);
      phone.setRole(Role.thief);
      expect(roles, [Role.thief]);
    });
  });

  group('pay', () {
    setUp(() async {
      await login();
      chip.signed.clear();
      chip.prompts.clear();
    });

    test(
      'authorising a large payment signs it but pays nothing until it is completed',
      () async {
        final authorisation = await c.authorise(25000, 'shop@upi');
        final draft = authorisation.draft!;
        expect(draft.steppedUp, isTrue);
        expect(chip.prompts.single, 'Pay ₹25,000|to shop@upi');
        expect(backend.confirmed, isEmpty);
        expect(backend.funds, 100000);
        expect(c.history, isEmpty);

        final r = await c.complete(draft);
        expect(r.ok, isTrue);
        expect(backend.confirmed.single, chip.signed.single);
        expect(c.balance, 75000);
      },
    );

    test(
      'authorising a small payment needs no signature and pays nothing yet',
      () async {
        final draft = (await c.authorise(500, 'chai@upi')).draft!;
        expect(draft.steppedUp, isFalse);
        expect(chip.signed, isEmpty);
        expect(backend.funds, 100000);
        expect((await c.complete(draft)).ok, isTrue);
        expect(backend.funds, 99500);
      },
    );

    test('backing out at the PIN screen sends nothing', () async {
      final draft = (await c.authorise(25000, 'shop@upi')).draft!;
      c.abandon(draft);
      expect(backend.confirmed, isEmpty);
      expect(backend.funds, 100000);
      expect(c.history, isEmpty);
      expect(c.busy, isFalse);
    });

    test('a small payment needs no signature', () async {
      final r = await c.pay(500, 'chai@upi');
      expect(r.ok, isTrue);
      expect(r.steppedUp, isFalse);
      expect(chip.signed, isEmpty);
      expect(c.balance, 99500);
      expect(c.history.single.result, r);
    });

    test(
      'a large payment signs the transaction and shows it in the prompt',
      () async {
        final r = await c.pay(25000, 'shop@upi');
        expect(r.ok, isTrue);
        expect(r.steppedUp, isTrue);
        expect(r.txnId, 'T-1');
        final signed = fields(chip.signed.single);
        expect(signed, containsPair('purpose', 'upi.pay'));
        expect(signed, containsPair('txnId', 'T-1'));
        expect(signed, containsPair('nonce', 'nonce-2'));
        expect(signed, containsPair('amount', 25000));
        expect(chip.prompts.single, 'Pay ₹25,000|to shop@upi');
        expect(backend.confirmed.single, chip.signed.single);
        expect(c.balance, 75000);
      },
    );

    test(
      'attack mode sends bytes the chip never signed, and a refusal is a blocked payment',
      () async {
        c.setTamper(true);
        backend.failConfirm = const ApiException(
          401,
          'bad_signature',
          'signature does not match the enrolled key',
          {},
        );
        final r = await c.pay(25000, 'shop@upi');
        expect(fields(chip.signed.single)['payee'], 'shop@upi');
        expect(fields(backend.confirmed.single)['payee'], 'evil@upi');
        expect(r.ok, isFalse);
        expect(r.blocked, isTrue);
        expect(r.steppedUp, isTrue);
        expect(r.txnId, 'T-1');
        expect(c.balance, 100000);
        expect(c.signedIn, isTrue);
      },
    );

    test('an invalidated key ends the session', () async {
      chip.signError = const ChipException('KEY_INVALIDATED', 'gone');
      final r = await c.pay(25000, 'shop@upi');
      expect(r.ok, isFalse);
      expect(r.code, 'KEY_INVALIDATED');
      expect(c.status.enrolled, isFalse);
      expect(c.signedIn, isFalse);
    });

    test('a cancelled prompt pays nothing and keeps the session', () async {
      chip.signError = const ChipException('CANCELLED', 'user cancelled');
      final r = await c.pay(25000, 'shop@upi');
      expect(r.ok, isFalse);
      expect(r.blocked, isFalse);
      expect(backend.confirmed, isEmpty);
      expect(c.signedIn, isTrue);
    });

    test('an expired session is reported and dropped', () async {
      backend.failNext = const ApiException(
        401,
        'token_invalid',
        'expired',
        {},
      );
      final r = await c.pay(500, 'chai@upi');
      expect(r.sessionExpired, isTrue);
      expect(c.signedIn, isFalse);
    });
  });

  test('reset deletes the key and the session', () async {
    await c.startLogin('9876543210');
    await c.reset();
    expect(c.status.enrolled, isFalse);
    expect(c.signedIn, isFalse);
    expect(c.verdict, isNull);
  });

  test(
    'changing the backend address tidies it, reports it once and drops nothing when unchanged',
    () {
      final reported = <String>[];
      final http = HttpBackend(
        'http://127.0.0.1:7447',
        onBaseUrlChanged: reported.add,
      );
      http.setBaseUrl('http://127.0.0.1:7447');
      http.setBaseUrl('   ');
      expect(reported, isEmpty);
      http.setBaseUrl(' http://10.0.0.5:7447// ');
      expect(http.baseUrl, 'http://10.0.0.5:7447');
      expect(reported, ['http://10.0.0.5:7447']);
      expect(http.hasToken, isFalse);
    },
  );
}
