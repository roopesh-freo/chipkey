import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../backend.dart';
import '../controller.dart';
import '../theme.dart';
import 'demo_controls.dart';
import 'mpin_screen.dart';
import 'result_screen.dart';

/// The UPI payment screen. Tapping Pay opens the payment. Above the
/// threshold the backend answers "sign it", and the fingerprint prompt
/// appears with the amount and payee. Then comes the demo PIN, and only
/// after it is the payment sent.
class HomeScreen extends StatefulWidget {
  const HomeScreen({super.key, required this.controller, this.backend});
  final DemoController controller;
  final HttpBackend? backend;

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> {
  final _amount = TextEditingController(text: '25000');
  // On the thief's phone the payee is his own, so the stage needs no typing.
  late final _payee = TextEditingController(
    text: c.role == Role.thief ? 'evil@upi' : 'shop@upi',
  );

  DemoController get c => widget.controller;

  @override
  void dispose() {
    _amount.dispose();
    _payee.dispose();
    super.dispose();
  }

  Future<void> _pay() async {
    final amount = int.tryParse(_amount.text.trim());
    final payee = _payee.text.trim();
    if (amount == null || amount <= 0 || payee.isEmpty) return;
    FocusScope.of(context).unfocus();
    final navigator = Navigator.of(context);

    // 1. Open the payment. For a large one the fingerprint prompt appears
    //    here, naming the amount and payee, and the chip signs.
    final authorisation = await _withProgress(() => c.authorise(amount, payee));
    if (!mounted) return;
    final draft = authorisation.draft;
    if (draft == null) {
      await navigator.push<void>(
        MaterialPageRoute(
          builder: (_) => ResultScreen(
            amount: amount,
            payee: payee,
            result: authorisation.failure!,
          ),
        ),
      );
      return;
    }

    // 2. The demo PIN.
    final pinEntered = await navigator.push<bool>(
      MaterialPageRoute(
        builder: (_) => MpinScreen(amount: amount, payee: payee),
      ),
    );
    if (!mounted) return;
    if (pinEntered != true) {
      c.abandon(draft);
      return;
    }

    // 3. Only now is anything sent for payment.
    final result = await _withProgress(() => c.complete(draft));
    if (!mounted) return;
    await navigator.push<void>(
      MaterialPageRoute(
        builder: (_) =>
            ResultScreen(amount: amount, payee: payee, result: result),
      ),
    );
  }

  /// Holds a progress sheet over the screen while [work] runs. The
  /// fingerprint prompt, when there is one, appears above it.
  Future<T> _withProgress<T>(Future<T> Function() work) async {
    final navigator = Navigator.of(context);
    showDialog<void>(
      context: context,
      barrierDismissible: false,
      builder: (_) => PopScope(canPop: false, child: _Progress(controller: c)),
    );
    try {
      return await work();
    } finally {
      if (mounted) navigator.pop();
    }
  }

  @override
  Widget build(BuildContext context) {
    final full = c.verdict?.fullLimits ?? true;
    final threshold = c.verdict?.threshold;
    return Scaffold(
      appBar: AppBar(
        title: const Wordmark(size: 28),
        actions: [
          IconButton(
            key: const Key('demoControls'),
            tooltip: 'Demo controls',
            onPressed: () => showDemoControls(context, c, widget.backend),
            icon: Icon(Icons.tune, color: c.tamper ? Brand.red : Brand.ink),
          ),
          IconButton(
            key: const Key('logout'),
            tooltip: 'Log out',
            onPressed: c.logout,
            icon: const Icon(Icons.logout),
          ),
        ],
      ),
      body: SafeArea(
        child: ListView(
          padding: const EdgeInsets.fromLTRB(16, 8, 16, 20),
          children: [
            Container(
              padding: const EdgeInsets.all(20),
              decoration: BoxDecoration(
                color: Brand.ink,
                borderRadius: BorderRadius.circular(18),
              ),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  const Text(
                    'Available balance',
                    style: TextStyle(color: Brand.greyLight, fontSize: 13),
                  ),
                  const SizedBox(height: 2),
                  Text(
                    c.balance == null ? '₹ ——' : rupees(c.balance!),
                    key: const Key('balance'),
                    style: const TextStyle(
                      color: Colors.white,
                      fontSize: 34,
                      fontWeight: FontWeight.w700,
                    ),
                  ),
                  const SizedBox(height: 12),
                  if (c.otpOnlySession)
                    const Pill(
                      key: Key('otpOnly'),
                      icon: Icons.sms_outlined,
                      text: 'Logged in by OTP · no device key',
                      color: Color(0xFFFF7070),
                      background: Color(0xFF2A2A2A),
                    )
                  else
                    Pill(
                      key: const Key('keyPill'),
                      icon: full
                          ? Icons.verified_user
                          : Icons.gpp_maybe_outlined,
                      text: full
                          ? 'Device key in ${(c.tier ?? 'hardware').toUpperCase()}'
                          : 'Unverified device · limit ${rupees(c.verdict!.reducedLimit)}',
                      color: full
                          ? const Color(0xFF6CD164)
                          : const Color(0xFFFFD842),
                      background: const Color(0xFF2A2A2A),
                    ),
                ],
              ),
            ),
            const SizedBox(height: 18),
            Container(
              padding: const EdgeInsets.all(16),
              decoration: BoxDecoration(
                color: Colors.white,
                borderRadius: BorderRadius.circular(18),
              ),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  const Text(
                    'Pay by UPI',
                    style: TextStyle(
                      color: Brand.ink,
                      fontSize: 18,
                      fontWeight: FontWeight.w600,
                    ),
                  ),
                  const SizedBox(height: 12),
                  TextField(
                    key: const Key('payee'),
                    controller: _payee,
                    autocorrect: false,
                    keyboardType: TextInputType.emailAddress,
                    style: const TextStyle(
                      fontSize: 17,
                      fontWeight: FontWeight.w500,
                    ),
                    decoration: const InputDecoration(
                      labelText: 'UPI ID',
                      prefixIcon: Icon(Icons.alternate_email, size: 20),
                    ),
                  ),
                  const SizedBox(height: 10),
                  TextField(
                    key: const Key('amount'),
                    controller: _amount,
                    keyboardType: TextInputType.number,
                    inputFormatters: [FilteringTextInputFormatter.digitsOnly],
                    style: const TextStyle(
                      fontSize: 26,
                      fontWeight: FontWeight.w700,
                    ),
                    decoration: const InputDecoration(
                      labelText: 'Amount',
                      prefixText: '₹ ',
                      prefixStyle: TextStyle(
                        color: Brand.ink,
                        fontSize: 26,
                        fontWeight: FontWeight.w700,
                      ),
                    ),
                  ),
                  const SizedBox(height: 10),
                  Row(
                    children: [
                      for (final preset in const [500, 25000])
                        Padding(
                          padding: const EdgeInsets.only(right: 8),
                          child: ActionChip(
                            key: Key('preset$preset'),
                            label: Text(rupees(preset)),
                            backgroundColor: Brand.coralTint,
                            side: BorderSide.none,
                            labelStyle: const TextStyle(
                              color: Brand.coralDark,
                              fontWeight: FontWeight.w600,
                            ),
                            onPressed: () =>
                                setState(() => _amount.text = '$preset'),
                          ),
                        ),
                    ],
                  ),
                  const SizedBox(height: 14),
                  BrandButton(
                    key: const Key('pay'),
                    label: 'Pay',
                    busy: c.busy,
                    onPressed: _pay,
                  ),
                  const SizedBox(height: 10),
                  Text(
                    c.otpOnlySession
                        ? 'This session came from an OTP. Nothing in this phone signs anything.'
                        : threshold == null
                        ? 'Large payments are signed by the secure chip in this phone.'
                        : 'Payments of ${rupees(threshold)} or more are signed by the secure chip in this phone.',
                    style: const TextStyle(
                      color: Brand.grey,
                      fontSize: 12,
                      height: 1.4,
                    ),
                  ),
                ],
              ),
            ),
            if (c.tamper) ...[
              const SizedBox(height: 12),
              Container(
                key: const Key('attackBanner'),
                padding: const EdgeInsets.all(12),
                decoration: BoxDecoration(
                  color: Brand.redTint,
                  borderRadius: BorderRadius.circular(12),
                ),
                child: const Row(
                  children: [
                    Icon(Icons.bug_report_outlined, color: Brand.red),
                    SizedBox(width: 10),
                    Expanded(
                      child: Text(
                        'Attack mode: the payee is rewritten to evil@upi after the chip signs.',
                        style: TextStyle(
                          color: Brand.red,
                          fontSize: 13,
                          fontWeight: FontWeight.w500,
                        ),
                      ),
                    ),
                  ],
                ),
              ),
            ],
            if (c.history.isNotEmpty) ...[
              const SizedBox(height: 18),
              const Text(
                'Recent payments',
                style: TextStyle(
                  color: Brand.ink,
                  fontSize: 16,
                  fontWeight: FontWeight.w600,
                ),
              ),
              const SizedBox(height: 6),
              for (final p in c.history.take(4))
                Container(
                  margin: const EdgeInsets.only(top: 6),
                  padding: const EdgeInsets.symmetric(
                    horizontal: 14,
                    vertical: 10,
                  ),
                  decoration: BoxDecoration(
                    color: Colors.white,
                    borderRadius: BorderRadius.circular(12),
                  ),
                  child: Row(
                    children: [
                      Icon(
                        p.result.ok ? Icons.north_east : Icons.block,
                        color: p.result.ok ? Brand.green : Brand.red,
                        size: 20,
                      ),
                      const SizedBox(width: 10),
                      Expanded(
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Text(
                              p.payee,
                              style: const TextStyle(
                                fontSize: 14,
                                fontWeight: FontWeight.w500,
                              ),
                            ),
                            Text(
                              p.result.ok
                                  ? (p.result.steppedUp
                                        ? 'Signed by the chip'
                                        : 'Session token')
                                  : (p.result.blocked
                                        ? 'Blocked by the backend'
                                        : 'Failed'),
                              style: const TextStyle(
                                color: Brand.grey,
                                fontSize: 12,
                              ),
                            ),
                          ],
                        ),
                      ),
                      Text(
                        rupees(p.amount),
                        style: TextStyle(
                          fontSize: 15,
                          fontWeight: FontWeight.w600,
                          color: p.result.ok ? Brand.ink : Brand.greyLight,
                          decoration: p.result.ok
                              ? null
                              : TextDecoration.lineThrough,
                        ),
                      ),
                    ],
                  ),
                ),
            ],
          ],
        ),
      ),
    );
  }
}

class _Progress extends StatelessWidget {
  const _Progress({required this.controller});
  final DemoController controller;

  @override
  Widget build(BuildContext context) {
    return Dialog(
      backgroundColor: Colors.white,
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(18)),
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 24, vertical: 28),
        child: ListenableBuilder(
          listenable: controller,
          builder: (context, _) => Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const SizedBox(
                width: 34,
                height: 34,
                child: CircularProgressIndicator(
                  strokeWidth: 3,
                  color: Brand.coral,
                ),
              ),
              const SizedBox(height: 18),
              Text(
                controller.phase.isEmpty ? 'Processing' : controller.phase,
                key: const Key('progress'),
                textAlign: TextAlign.center,
                style: const TextStyle(
                  color: Brand.ink,
                  fontSize: 16,
                  fontWeight: FontWeight.w500,
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
