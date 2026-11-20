import 'package:demo/backend.dart';
import 'package:demo/controller.dart';
import 'package:demo/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'fakes.dart';

void main() {
  late FakeChip chip;
  late FakeBackend backend;
  late DemoController c;

  /// Opens the app on one phone. The timer is off: tests call [poll].
  Future<void> open(
    WidgetTester tester, {
    FakeServer? server,
    Role role = Role.owner,
    String id = 'd-test',
    String name = 'Test Phone',
  }) async {
    // A OnePlus-sized screen: 1080 x 2400 at density 3.
    tester.view.physicalSize = const Size(1080, 2400);
    tester.view.devicePixelRatio = 3;
    addTearDown(tester.view.reset);
    chip = FakeChip(id: id, name: name);
    backend = FakeBackend(server);
    c = DemoController(
      chip: chip,
      backend: backend,
      role: role,
      pollEvery: null,
    );
    await tester.pumpWidget(ChipkeyApp(controller: c));
    await tester.pumpAndSettle();
  }

  /// One round of the app's polling, then a frame.
  Future<void> poll(WidgetTester tester, [DemoController? who]) async {
    await (who ?? c).tick();
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 300));
  }

  /// A second phone with no screen, on the same verifier.
  Future<DemoController> otherPhone(
    FakeServer server, {
    required String id,
    required String name,
    Role role = Role.owner,
  }) async {
    final phone = DemoController(
      chip: FakeChip(id: id, name: name),
      backend: FakeBackend(server),
      role: role,
      pollEvery: null,
    );
    await phone.init();
    await phone.tick();
    return phone;
  }

  /// Types the number, reads the forwarded OTP off the banner, submits it.
  Future<void> otpFromBanner(WidgetTester tester) async {
    await tester.tap(find.byKey(const Key('continue')));
    await tester.pumpAndSettle();
    expect(find.text('Enter the OTP'), findsOneWidget);
    await poll(tester);
    expect(find.byKey(const Key('smsBanner')), findsOneWidget);
    await tester.tap(find.byKey(const Key('smsText')));
    await tester.pump();
    expect(find.byKey(const Key('smsBanner')), findsNothing);
    await tester.tap(find.byKey(const Key('verify')));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 300));
  }

  Future<void> logIn(WidgetTester tester) async {
    await tester.tap(find.byKey(const Key('continue')));
    await tester.pumpAndSettle();
  }

  Future<void> enterPin(WidgetTester tester) async {
    expect(find.text('Enter UPI PIN'), findsOneWidget);
    for (final digit in ['1', '2', '3', '4']) {
      await tester.tap(find.byKey(Key('pin-$digit')));
      await tester.pump();
    }
    await tester.pumpAndSettle();
  }

  testWidgets('login screen binds the phone and opens the payment screen', (
    tester,
  ) async {
    await open(tester);
    expect(find.text('Log in to continue'), findsOneWidget);
    expect(find.textContaining('holds no device key yet'), findsOneWidget);
    expect(find.byKey(const Key('pay')), findsNothing);

    await logIn(tester);
    expect(chip.enrolCount, 1);
    expect(find.text('Pay by UPI'), findsOneWidget);
    expect(find.text('₹1,00,000'), findsOneWidget);
    expect(find.textContaining('Device key in TEE'), findsOneWidget);
    // The Pay button is on screen without scrolling.
    expect(tester.getRect(find.byKey(const Key('pay'))).bottom, lessThan(800));
  });

  testWidgets('a failed login stays on the login screen and says why', (
    tester,
  ) async {
    await open(tester);
    chip.signError = const FakeCancel();
    await logIn(tester);
    expect(find.byKey(const Key('loginError')), findsOneWidget);
    expect(find.textContaining('cancelled'), findsOneWidget);
    expect(find.text('Pay by UPI'), findsNothing);
  });

  testWidgets('a large payment goes fingerprint, PIN, success', (tester) async {
    await open(tester);
    await logIn(tester);
    chip.prompts.clear();

    await tester.tap(find.byKey(const Key('pay')));
    await tester.pumpAndSettle();

    // The chip has signed before the PIN screen opens, and nothing is paid.
    expect(chip.prompts.single, 'Pay ₹25,000|to shop@upi');
    expect(find.text('Paying shop@upi'), findsOneWidget);
    expect(backend.confirmed, isEmpty);
    expect(backend.funds, 100000);

    await enterPin(tester);
    expect(backend.confirmed, hasLength(1));
    expect(find.text('Payment successful'), findsOneWidget);
    expect(
      find.textContaining('signed inside the secure chip'),
      findsOneWidget,
    );
    expect(find.text('Transaction T-1'), findsOneWidget);

    await tester.tap(find.byKey(const Key('done')));
    await tester.pumpAndSettle();
    expect(find.text('₹75,000'), findsOneWidget);
    expect(find.text('Signed by the chip'), findsOneWidget);
  });

  testWidgets('a cancelled fingerprint never reaches the PIN screen', (
    tester,
  ) async {
    await open(tester);
    await logIn(tester);
    chip.signError = const FakeCancel();

    await tester.tap(find.byKey(const Key('pay')));
    await tester.pumpAndSettle();
    expect(find.text('Enter UPI PIN'), findsNothing);
    expect(find.text('Payment failed'), findsOneWidget);
    expect(find.textContaining('cancelled'), findsOneWidget);
    expect(backend.funds, 100000);
  });

  testWidgets('a small payment needs the PIN but no fingerprint', (
    tester,
  ) async {
    await open(tester);
    await logIn(tester);
    chip.prompts.clear();

    await tester.tap(find.byKey(const Key('preset500')));
    await tester.pump();
    await tester.tap(find.byKey(const Key('pay')));
    await tester.pumpAndSettle();
    await enterPin(tester);

    expect(chip.prompts, isEmpty);
    expect(find.text('Payment successful'), findsOneWidget);
    expect(
      find.textContaining('approved on your session token'),
      findsOneWidget,
    );
  });

  testWidgets('closing the PIN screen sends nothing', (tester) async {
    await open(tester);
    await logIn(tester);
    await tester.tap(find.byKey(const Key('pay')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('pinClose')));
    await tester.pumpAndSettle();
    expect(find.text('Pay by UPI'), findsOneWidget);
    expect(backend.funds, 100000);
    expect(backend.confirmed, isEmpty);
  });

  testWidgets('attack mode ends on a blocked screen', (tester) async {
    await open(tester);
    await logIn(tester);

    await tester.tap(find.byKey(const Key('demoControls')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('tamper')));
    await tester.pumpAndSettle();
    await tester.tapAt(const Offset(180, 60)); // dismiss the sheet
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('attackBanner')), findsOneWidget);

    backend.failConfirm = const ApiException(
      401,
      'bad_signature',
      'signature does not match the enrolled key',
      {},
    );
    await tester.tap(find.byKey(const Key('pay')));
    await tester.pumpAndSettle();
    await enterPin(tester);

    expect(find.text('Payment blocked'), findsOneWidget);
    expect(find.textContaining('not what this phone signed'), findsOneWidget);
    await tester.tap(find.byKey(const Key('done')));
    await tester.pumpAndSettle();
    expect(find.text('₹1,00,000'), findsOneWidget);
    await tester.scrollUntilVisible(
      find.text('Blocked by the backend'),
      120,
      scrollable: find
          .descendant(
            of: find.byType(ListView),
            matching: find.byType(Scrollable),
          )
          .first,
    );
    expect(find.text('Blocked by the backend'), findsOneWidget);
  });

  testWidgets(
    'logging out returns to the login screen with the phone still bound',
    (tester) async {
      await open(tester);
      await logIn(tester);
      await tester.tap(find.byKey(const Key('logout')));
      await tester.pumpAndSettle();
      expect(find.text('Log in to continue'), findsOneWidget);
      expect(find.textContaining('holds a device key in TEE'), findsOneWidget);
    },
  );

  group('the night as it happened', () {
    testWidgets('the forwarded OTP opens the account and pays from it', (
      tester,
    ) async {
      final server = FakeServer()..legacy = true;
      await open(
        tester,
        server: server,
        role: Role.thief,
        id: 'd-thief',
        name: 'Google Pixel 9',
      );
      await poll(tester);

      await tester.tap(find.byKey(const Key('continue')));
      await tester.pumpAndSettle();
      expect(find.text('Enter the OTP'), findsOneWidget);
      expect(find.textContaining('••3210'), findsOneWidget);

      await poll(tester);
      expect(find.textContaining('FORWARDED FROM HER PHONE'), findsOneWidget);
      expect(find.textContaining(server.code!), findsOneWidget);
      expect(find.text('Tap to use this code'), findsOneWidget);
      await tester.tap(find.byKey(const Key('smsText')));
      await tester.pump();
      expect(
        tester.widget<TextField>(find.byKey(const Key('otp'))).controller!.text,
        server.code,
      );

      await tester.tap(find.byKey(const Key('verify')));
      await tester.pumpAndSettle();
      expect(find.text('Pay by UPI'), findsOneWidget);
      expect(find.byKey(const Key('otpOnly')), findsOneWidget);
      expect(find.byKey(const Key('keyPill')), findsNothing);
      expect(chip.enrolCount, 0);

      // ₹25,000 with no fingerprint: the session token is all it takes.
      await tester.tap(find.byKey(const Key('pay')));
      await tester.pumpAndSettle();
      await enterPin(tester);
      expect(find.text('Payment successful'), findsOneWidget);
      expect(chip.prompts, isEmpty);
      expect(server.funds, 75000);
    });

    testWidgets('her own phone shows the SMS as an ordinary message', (
      tester,
    ) async {
      final server = FakeServer()..legacy = true;
      await open(tester, server: server);
      await poll(tester);
      await tester.tap(find.byKey(const Key('continue')));
      await tester.pumpAndSettle();
      await poll(tester);
      expect(find.textContaining('Messages ·'), findsOneWidget);
      expect(find.textContaining('FORWARDED'), findsNothing);
      await tester.tap(find.byKey(const Key('smsDismiss')));
      await tester.pump();
      expect(find.byKey(const Key('smsBanner')), findsNothing);
    });

    testWidgets('a wrong OTP stays on the OTP screen', (tester) async {
      final server = FakeServer()..legacy = true;
      await open(tester, server: server);
      await tester.tap(find.byKey(const Key('continue')));
      await tester.pumpAndSettle();
      await tester.enterText(find.byKey(const Key('otp')), '000000');
      await tester.tap(find.byKey(const Key('verify')));
      await tester.pumpAndSettle();
      expect(find.textContaining('wrong or has expired'), findsOneWidget);
      await tester.tap(find.byKey(const Key('changeNumber')));
      await tester.pumpAndSettle();
      expect(find.text('Log in to continue'), findsOneWidget);
    });
  });

  group('the same night with the chip', () {
    late FakeServer server;
    late DemoController meera;

    /// Meera bound her phone long before tonight.
    Future<void> meeraIsBound() async {
      server = FakeServer();
      meera = await otherPhone(server, id: 'd-meera', name: 'OnePlus IV2201');
      await meera.startLogin('9876543210');
      expect(meera.signedIn, isTrue);
    }

    testWidgets('his phone has the OTP and a real chip, and still waits', (
      tester,
    ) async {
      await meeraIsBound();
      await open(
        tester,
        server: server,
        role: Role.thief,
        id: 'd-thief',
        name: 'Google Pixel 9',
      );
      await poll(tester);
      await otpFromBanner(tester);

      expect(find.byKey(const Key('waitingTitle')), findsOneWidget);
      expect(find.textContaining('not enough to get in'), findsOneWidget);
      expect(find.text('Pay by UPI'), findsNothing);
      expect(chip.enrolCount, 1);

      // She is asleep. Nobody answers, and the request dies.
      server.asks.single.status = 'expired';
      await poll(tester);
      expect(find.byKey(const Key('refusedTitle')), findsOneWidget);
      expect(find.textContaining('did not approve'), findsOneWidget);

      // His last idea.
      await tester.tap(find.byKey(const Key('lost')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('recoveryTitle')), findsOneWidget);
      expect(find.textContaining('live face check'), findsOneWidget);
      expect(server.recoveries, ['d-thief']);
      expect(find.text('Pay by UPI'), findsNothing);

      await tester.tap(find.byKey(const Key('back')));
      await tester.pumpAndSettle();
      expect(find.text('Log in to continue'), findsOneWidget);
    });

    testWidgets('her phone is asked, and one tap blocks him', (tester) async {
      server = FakeServer();
      await open(tester, server: server, id: 'd-meera', name: 'OnePlus IV2201');
      await logIn(tester);
      expect(find.text('Pay by UPI'), findsOneWidget);
      chip.prompts.clear();

      final thief = await otherPhone(
        server,
        id: 'd-thief',
        name: 'Google Pixel 9',
        role: Role.thief,
      );
      await thief.startLogin('9876543210');
      await thief.tick();
      await thief.submitOtp(thief.banner!.code!);
      expect(thief.stage, LoginStage.waiting);

      await poll(tester);
      expect(find.byKey(const Key('approvalCard')), findsOneWidget);
      expect(find.text('Google Pixel 9'), findsOneWidget);
      await tester.tap(find.byKey(const Key('block')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('approvalCard')), findsNothing);
      expect(chip.prompts, isEmpty);

      await thief.tick();
      expect(thief.stage, LoginStage.refused);
      expect(thief.signedIn, isFalse);
    });

    testWidgets('her own new phone is let in with her fingerprint', (
      tester,
    ) async {
      server = FakeServer();
      await open(tester, server: server, id: 'd-meera', name: 'OnePlus IV2201');
      await logIn(tester);
      chip.prompts.clear();

      final fresh = await otherPhone(
        server,
        id: 'd-new',
        name: 'Google Pixel 9',
      );
      await fresh.startLogin('9876543210');
      await fresh.tick();
      await fresh.submitOtp(fresh.banner!.code!);

      await poll(tester);
      await tester.tap(find.byKey(const Key('approve')));
      await tester.pumpAndSettle();
      expect(chip.prompts.single, 'Approve new device|Google Pixel 9');
      expect(find.byKey(const Key('approvalCard')), findsNothing);

      await fresh.tick();
      expect(fresh.signedIn, isTrue);
    });
  });

  testWidgets('the demo controls switch which character this phone plays', (
    tester,
  ) async {
    await open(tester);
    expect(c.role, Role.owner);
    await tester.tap(find.byKey(const Key('demoControls')));
    await tester.pumpAndSettle();
    expect(find.textContaining('Her SIM is in this phone'), findsOneWidget);
    await tester.tap(find.text("Thief's phone"));
    await tester.pumpAndSettle();
    expect(c.role, Role.thief);
    expect(find.textContaining('her phone forwards'), findsOneWidget);
  });

  testWidgets(
    'a new backend address is saved, shown on the login screen and ends the session',
    (tester) async {
      tester.view.physicalSize = const Size(1080, 2400);
      tester.view.devicePixelRatio = 3;
      addTearDown(tester.view.reset);
      final saved = <String>[];
      final http = HttpBackend(defaultBackendUrl, onBaseUrlChanged: saved.add);
      chip = FakeChip();
      await tester.pumpWidget(
        ChipkeyApp(
          controller: DemoController(
            chip: chip,
            backend: http,
            pollEvery: null,
          ),
          backend: http,
        ),
      );
      await tester.pumpAndSettle();
      expect(
        find.textContaining('This is not the real Freo app'),
        findsOneWidget,
      );

      await tester.tap(find.byKey(const Key('demoControls')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('backendAddress')));
      await tester.pumpAndSettle();
      await tester.enterText(
        find.byKey(const Key('backendField')),
        ' http://192.168.1.20:7447/ ',
      );
      await tester.tap(find.byKey(const Key('backendSave')));
      await tester.pumpAndSettle();

      expect(http.baseUrl, 'http://192.168.1.20:7447');
      expect(saved, ['http://192.168.1.20:7447']);
      expect(
        find.text('Demo app. Verifier at 192.168.1.20:7447'),
        findsOneWidget,
      );

      // One tap goes back to the USB address.
      await tester.tap(find.byKey(const Key('demoControls')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('backendAddress')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('backendUsb')));
      await tester.pumpAndSettle();
      expect(http.baseUrl, defaultBackendUrl);
      expect(saved.last, defaultBackendUrl);
      expect(
        find.textContaining('This is not the real Freo app'),
        findsOneWidget,
      );
    },
  );
}
