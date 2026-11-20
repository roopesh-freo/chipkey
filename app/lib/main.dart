import 'package:flutter/material.dart';

import 'backend.dart';
import 'chip.dart';
import 'controller.dart';
import 'screens/home_screen.dart';
import 'screens/login_screen.dart';
import 'screens/overlays.dart';
import 'settings.dart';
import 'theme.dart';

/// Where a fresh install looks for the verifier. A build for a hosted
/// verifier sets it with --dart-define=CHIPKEY_BACKEND=http://host:7447;
/// without that the app starts on the USB address.
const _startBackendUrl = String.fromEnvironment(
  'CHIPKEY_BACKEND',
  defaultValue: defaultBackendUrl,
);

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  // The backend address chosen in the demo controls survives a restart.
  final settings = MethodChannelSettings();
  final saved = await settings.read('backendUrl');
  final backend = HttpBackend(
    saved == null || saved.isEmpty ? _startBackendUrl : saved,
    onBaseUrlChanged: (url) => settings.write('backendUrl', url),
  );
  // So does the part this handset plays: Meera's phone or the thief's.
  final role = await settings.read('role') == 'thief' ? Role.thief : Role.owner;
  runApp(
    ChipkeyApp(
      controller: DemoController(
        chip: MethodChannelKeyChip(),
        backend: backend,
        role: role,
        onRoleChanged: (r) => settings.write('role', r.name),
      ),
      backend: backend,
    ),
  );
}

class ChipkeyApp extends StatefulWidget {
  const ChipkeyApp({super.key, required this.controller, this.backend});

  final DemoController controller;

  /// Present when the backend address can be edited from the demo controls.
  final HttpBackend? backend;

  @override
  State<ChipkeyApp> createState() => _ChipkeyAppState();
}

class _ChipkeyAppState extends State<ChipkeyApp> {
  @override
  void initState() {
    super.initState();
    widget.controller.init();
  }

  @override
  Widget build(BuildContext context) {
    final c = widget.controller;
    return MaterialApp(
      title: 'Chipkey Demo',
      debugShowCheckedModeBanner: false,
      theme: brandTheme(),
      // An SMS, or another phone asking to be let in, can arrive on any
      // screen, so both are drawn above the navigator.
      builder: (context, child) => ListenableBuilder(
        listenable: c,
        builder: (context, _) => Stack(
          children: [
            ?child,
            if (c.incoming != null)
              Positioned.fill(child: ApprovalCard(controller: c)),
            if (c.banner != null)
              Positioned(
                top: 0,
                left: 0,
                right: 0,
                child: SmsBanner(controller: c),
              ),
          ],
        ),
      ),
      // The session token decides which screen is the root. Logging out, an
      // expired session or a dead key all land back on the login screen.
      home: ListenableBuilder(
        listenable: c,
        builder: (context, _) => c.signedIn
            ? HomeScreen(controller: c, backend: widget.backend)
            : LoginScreen(controller: c, backend: widget.backend),
      ),
    );
  }
}
