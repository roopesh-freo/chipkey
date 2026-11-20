import 'dart:async';
import 'dart:io';

import 'package:flutter/foundation.dart';

import 'backend.dart';
import 'chip.dart';
import 'payloads.dart';

enum LogKind { step, ok, warn, bad }

class LogLine {
  LogLine(this.kind, this.text, this.at);
  final LogKind kind;
  final String text;
  final DateTime at;
}

/// Formats rupees with Indian digit grouping: 1,25,000.
String rupees(int n) {
  final s = n.toString();
  if (s.length <= 3) return '₹$s';
  var head = s.substring(0, s.length - 3);
  final groups = <String>[];
  while (head.length > 2) {
    groups.insert(0, head.substring(head.length - 2));
    head = head.substring(0, head.length - 2);
  }
  groups.insert(0, head);
  return '₹${groups.join(',')},${s.substring(s.length - 3)}';
}

/// Keeps the last ten digits, so "+91 98765 43210" and "9876543210" match.
String normalizePhone(String input) {
  final digits = input.replaceAll(RegExp(r'\D'), '');
  return digits.length > 10 ? digits.substring(digits.length - 10) : digits;
}

/// How a payment attempt ended.
class PayResult {
  const PayResult.paid({
    required this.txnId,
    required this.balance,
    required this.steppedUp,
  }) : ok = true,
       code = 'paid',
       message = '';
  const PayResult.failed(
    this.code,
    this.message, {
    this.steppedUp = false,
    this.txnId,
  }) : ok = false,
       balance = null;

  final bool ok;
  final String code;
  final String message;
  final String? txnId;
  final int? balance;

  /// True when the backend demanded a signature from the chip.
  final bool steppedUp;

  /// The backend caught a payload that does not match what the chip signed.
  bool get blocked => code == 'bad_signature' || code == 'payload_mismatch';
  bool get sessionExpired => code == 'token_invalid';
}

/// A payment that has been authorised on the phone and is waiting for the
/// PIN. For a large payment it carries what the chip signed.
class PaymentDraft {
  PaymentDraft({
    required this.amount,
    required this.payee,
    required this.txnId,
    this.payload,
    this.signature,
  });
  final int amount;
  final String payee;
  final String txnId;
  final Uint8List? payload;
  final Uint8List? signature;
  bool get steppedUp => signature != null;
}

/// The outcome of [DemoController.authorise]: a draft to take to the PIN
/// screen, or the reason there is none.
class Authorisation {
  const Authorisation.ready(PaymentDraft this.draft) : failure = null;
  const Authorisation.failed(PayResult this.failure) : draft = null;
  final PaymentDraft? draft;
  final PayResult? failure;
}

/// One row of the home screen's recent payments.
class Payment {
  Payment(this.amount, this.payee, this.result);
  final int amount;
  final String payee;
  final PayResult result;
}

/// Where a login has got to.
enum LoginStage {
  /// Asking for the phone number.
  idle,

  /// The backend sent an OTP and wants it typed in.
  otp,

  /// This is a new phone for the account. The old phone has been asked.
  waiting,

  /// The old phone said no, or said nothing.
  refused,

  /// "I lost my phone": the slow path.
  recovery,
}

/// Which character this handset plays on stage.
enum Role {
  /// Meera. Her SIM is in this phone, so OTPs arrive here.
  owner,

  /// The thief. He sees the copies her phone forwards.
  thief,
}

/// One account, reached by a phone number, on two nights.
///
/// With the old app the backend answers a login with "type the OTP", and
/// whoever has the OTP is in. With the chip, [startLogin] ends in a signature
/// from the key in this phone, and a phone the account has never seen has to
/// wait for the old phone to sign for it.
class DemoController extends ChangeNotifier {
  DemoController({
    required this.chip,
    required this.backend,
    DateTime Function()? clock,
    this.role = Role.owner,
    this.onRoleChanged,
    this.pollEvery = const Duration(milliseconds: 1500),
  }) : _clock = clock ?? DateTime.now;

  final KeyChip chip;
  final BackendApi backend;
  final DateTime Function() _clock;
  final void Function(Role role)? onRoleChanged;

  /// How often to look for SMS, approval requests and approval answers.
  /// Null turns the timer off, for tests that drive [tick] by hand.
  final Duration? pollEvery;

  String deviceId = '';
  String deviceName = 'Android phone';
  String phone = '9876543210';
  ChipStatus status = const ChipStatus(
    enrolled: false,
    biometrics: 'unavailable',
  );
  EnrolVerdict? verdict;
  int? balance;
  bool busy = false;
  Role role;

  /// What the app is doing right now, in words for a progress indicator.
  String phase = '';

  /// Why the last login step failed, if it did.
  String? error;

  LoginStage stage = LoginStage.idle;
  String refusedReason = '';
  String? _approvalId;

  /// True when this session came from an OTP alone, with no device key.
  bool otpOnlySession = false;

  /// A new phone asking this phone to let it into the account.
  ApprovalRequest? incoming;

  /// The newest SMS, shown as a banner for a few seconds.
  SmsMessage? banner;
  DateTime? bannerAt;
  String? _lastSms;

  /// How long an SMS banner stays up.
  static const bannerFor = Duration(seconds: 12);

  /// A code picked from the SMS banner, for the OTP field to show.
  String otpDraft = '';

  /// Demo switch: rewrite the payee after the chip has signed.
  bool tamper = false;

  final List<Payment> history = [];
  final List<LogLine> log = [];
  Timer? _timer;
  bool _ticking = false;

  bool get signedIn => backend.hasToken;

  /// The tier the backend decided on, or the phone's own report before that.
  String? get tier => verdict?.tier ?? status.tier;

  void _say(LogKind kind, String text) {
    log.insert(0, LogLine(kind, text, _clock()));
    notifyListeners();
  }

  void _phase(String text) {
    phase = text;
    _say(LogKind.step, text);
  }

  void setTamper(bool value) {
    tamper = value;
    notifyListeners();
  }

  void setRole(Role value) {
    if (role == value) return;
    role = value;
    banner = null;
    _lastSms = null;
    onRoleChanged?.call(value);
    notifyListeners();
  }

  Future<void> init() async {
    try {
      deviceId = await chip.deviceId();
      deviceName = await chip.deviceName();
      status = await chip.status();
    } on ChipException catch (e) {
      error = e.message;
    }
    final every = pollEvery;
    if (every != null) _timer = Timer.periodic(every, (_) => tick());
    notifyListeners();
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }

  // ---- login ---------------------------------------------------------------

  /// Asks the backend what this phone must do to get into [phoneInput]'s
  /// account, and does as much of it as needs no typing.
  Future<void> startLogin(String phoneInput) => _guarded(() async {
    final number = normalizePhone(phoneInput);
    if (number.length != 10) {
      throw const ChipException(
        'BAD_NUMBER',
        'Enter a ten-digit mobile number.',
      );
    }
    phone = number;
    stage = LoginStage.idle;
    otpDraft = '';
    otpOnlySession = false;
    status = await chip.status();
    _phase('Asking how to log in');
    await _follow(
      await backend.loginStart(phone, deviceId, hasKey: status.enrolled),
    );
  });

  /// Submits the OTP the user typed.
  Future<void> submitOtp(String code) => _guarded(() async {
    _phase('Checking the OTP');
    await _follow(
      await backend.loginOtp(phone, deviceId, deviceName, code.trim()),
    );
  });

  Future<void> _follow(LoginStep step) async {
    switch (step.next) {
      case 'otp':
        stage = LoginStage.otp;
        _say(LogKind.step, 'The backend sent an OTP to ${_masked()}');
      case 'token':
        // The old app: the OTP was the whole login.
        stage = LoginStage.idle;
        otpOnlySession = true;
        verdict = null;
        _say(LogKind.ok, 'OTP accepted. Logged in, with no device key.');
        balance = await backend.balance();
      case 'enrol':
        await _bind(step);
      case 'sign':
        await _signIn(step.nonce!);
      default:
        throw ChipException(
          'PROTOCOL',
          'The backend asked for "${step.next}", which this app does not know.',
        );
    }
  }

  /// Makes a key in the chip and proves it to the backend.
  Future<void> _bind(LoginStep step) async {
    if (status.biometrics != 'ready') {
      throw const ChipException(
        'NO_BIOMETRICS',
        'Add a fingerprint and a screen lock in Settings first.',
      );
    }
    _phase('Binding this device');
    final enrolment = await chip.enrol(step.challenge!);
    status = await chip.status();
    if (enrolment.fellBackFromStrongBox) {
      _say(
        LogKind.warn,
        'StrongBox is not available on this phone. Retried without it.',
      );
    }
    _say(
      LogKind.ok,
      'Key created. Android reports tier ${enrolment.tier.toUpperCase()}.',
    );

    _phase('Proving the key to the backend');
    final v = await backend.enrol(
      deviceId,
      enrolment.chain,
      phone: phone,
      deviceName: deviceName,
    );
    verdict = v;
    balance = null;
    final approval = v.approvalId;
    if (approval != null) {
      // A genuine key in a genuine chip, and still not in: the account
      // belongs to another phone, and only that phone can vouch for this one.
      _approvalId = approval;
      stage = LoginStage.waiting;
      _say(
        LogKind.warn,
        'This account is bound to another phone. Waiting for it to approve.',
      );
      return;
    }
    _say(
      v.fullLimits ? LogKind.ok : LogKind.warn,
      v.fullLimits
          ? 'Backend verified the chain. Tier ${v.tier.toUpperCase()}, full limits.'
          : 'Backend enrolled this phone as ${v.tier.toUpperCase()} with reduced limits of ${rupees(v.reducedLimit)}.',
    );
    await _follow(await backend.loginStart(phone, deviceId, hasKey: true));
  }

  /// Signs the backend's nonce with the chip and takes the session token.
  Future<void> _signIn(String nonce) async {
    final payload = loginPayload(
      deviceId: deviceId,
      keyId: _requireKey(),
      nonce: nonce,
      now: _clock(),
    );
    _phase('Confirm with your fingerprint');
    final Uint8List signature;
    try {
      signature = await chip.sign(
        payload,
        title: 'Log in',
        subtitle: 'Unlock your device key',
      );
    } on ChipException catch (e) {
      if (e.code != 'KEY_INVALIDATED') rethrow;
      // A new fingerprint or a lock screen change killed the key. The same
      // phone binds again: the backend sees it has no key and starts over.
      _say(
        LogKind.bad,
        'The device key was invalidated by a biometric change. Binding again.',
      );
      status = await chip.status();
      verdict = null;
      await _follow(await backend.loginStart(phone, deviceId, hasKey: false));
      return;
    }
    _phase('Verifying the signature');
    final ttl = await backend.login(deviceId, payload, signature);
    stage = LoginStage.idle;
    _say(
      LogKind.ok,
      'Signature accepted. X-Token issued for ${ttl.inMinutes} minutes.',
    );
    balance = await backend.balance();
  }

  /// "I lost my phone." The backend answers with the slow path.
  Future<void> lostMyPhone() => _guarded(() async {
    await backend.startRecovery(phone, deviceId);
    stage = LoginStage.recovery;
    _say(
      LogKind.warn,
      'Recovery asks for a live face check and a day of low limits. An OTP is not enough.',
    );
  });

  /// Returns to the phone number screen.
  void backToStart() {
    stage = LoginStage.idle;
    error = null;
    _approvalId = null;
    notifyListeners();
  }

  // ---- the old phone's side ------------------------------------------------

  /// Lets the waiting phone in, with a signature from this phone's chip.
  Future<void> approveIncoming() => _guarded(() async {
    final request = incoming;
    if (request == null) return;
    final payload = approvePayload(
      approvalId: request.id,
      newKeyId: request.newKeyId,
      nonce: request.nonce,
      deviceId: deviceId,
      keyId: _requireKey(),
      now: _clock(),
    );
    _phase('Confirm with your fingerprint');
    final signature = await chip.sign(
      payload,
      title: 'Approve new device',
      subtitle: request.deviceName,
    );
    await backend.decideApproval(
      request.id,
      deviceId,
      approve: true,
      payload: payload,
      signature: signature,
    );
    incoming = null;
    _say(LogKind.ok, '${request.deviceName} was approved by this phone.');
  });

  Future<void> blockIncoming() => _guarded(() async {
    final request = incoming;
    if (request == null) return;
    await backend.decideApproval(request.id, deviceId, approve: false);
    incoming = null;
    _say(LogKind.ok, '${request.deviceName} was blocked.');
  });

  // ---- polling -------------------------------------------------------------

  /// One round of looking for news: an SMS, a phone asking to join, or an
  /// answer for this phone. Errors are ignored: the next round tries again.
  Future<void> tick() async {
    if (_ticking || deviceId.isEmpty) return;
    _ticking = true;
    try {
      await _pollSms();
      if (status.enrolled && stage != LoginStage.waiting) await _pollIncoming();
      if (stage == LoginStage.waiting && !busy) await _pollApproval();
      if (signedIn && !busy) await _pollBalance();
    } on Exception {
      // Offline for a moment. Nothing to do.
    } finally {
      _ticking = false;
    }
  }

  Future<void> _pollSms() async {
    final shownAt = bannerAt;
    if (banner != null &&
        shownAt != null &&
        _clock().difference(shownAt) > bannerFor) {
      banner = null;
      notifyListeners();
    }
    // The whole (short) list every time, compared by id and text: after
    // the verifier is rewound the ids start again, but the codes differ.
    final messages = await backend.sms(phone, forwarded: role == Role.thief);
    final newest = messages.isEmpty
        ? ''
        : '${messages.last.id}|${messages.last.text}';
    final seen = _lastSms;
    _lastSms = newest;
    // Messages from before the app opened are history, not news.
    if (seen == null || newest == seen || messages.isEmpty) return;
    banner = messages.last;
    bannerAt = _clock();
    notifyListeners();
  }

  /// Keeps the balance honest when the dashboard rewinds the night, and
  /// sends the phone back to the login screen if its session was wiped.
  Future<void> _pollBalance() async {
    try {
      final now = await backend.balance();
      if (!busy && now != balance) {
        balance = now;
        notifyListeners();
      }
    } on ApiException catch (e) {
      if (e.code != 'token_invalid' || busy) return;
      backend.dropToken();
      balance = null;
      otpOnlySession = false;
      history.clear();
      notifyListeners();
    }
  }

  Future<void> _pollIncoming() async {
    final list = await backend.pendingApprovals(deviceId);
    final next = list.isEmpty ? null : list.first;
    if (next?.id != incoming?.id) {
      incoming = next;
      notifyListeners();
    }
  }

  Future<void> _pollApproval() async {
    final id = _approvalId;
    if (id == null) return;
    final answer = await backend.approvalStatus(id);
    if (answer == 'pending') return;
    _approvalId = null;
    if (answer == 'approved') {
      _say(LogKind.ok, 'The old phone approved this one.');
      await _guarded(
        () async =>
            _follow(await backend.loginStart(phone, deviceId, hasKey: true)),
      );
      return;
    }
    refusedReason = answer == 'blocked'
        ? 'The phone that owns this account blocked the request.'
        : 'The phone that owns this account did not approve it.';
    stage = LoginStage.refused;
    _say(LogKind.bad, refusedReason);
  }

  void dismissBanner() {
    banner = null;
    notifyListeners();
  }

  /// Tapping the SMS banner: the code goes into the OTP field.
  void useBannerCode() {
    final code = banner?.code;
    banner = null;
    if (code != null && stage == LoginStage.otp) otpDraft = code;
    notifyListeners();
  }

  // ---- payments ------------------------------------------------------------

  /// Act 3, first half. Opens the payment. If the backend asks for
  /// step-up, the chip signs the transaction after a fingerprint match.
  /// Nothing is paid yet: the PIN screen comes next.
  Future<Authorisation> authorise(int amount, String payee) async {
    if (busy) {
      return const Authorisation.failed(
        PayResult.failed('busy', 'Another request is still running.'),
      );
    }
    busy = true;
    notifyListeners();
    String? txnId;
    var steppedUp = false;
    try {
      _phase('Starting ${rupees(amount)} to $payee');
      final intent = await backend.startPayment(amount, payee);
      txnId = intent.txnId;
      final nonce = intent.nonce;
      if (nonce == null) {
        _say(
          LogKind.ok,
          'No step-up needed for ${intent.txnId}. The session token is enough.',
        );
        return Authorisation.ready(
          PaymentDraft(amount: amount, payee: payee, txnId: intent.txnId),
        );
      }

      steppedUp = true;
      _say(
        LogKind.warn,
        'Backend wants step-up for ${intent.txnId}: sign the transaction itself',
      );
      final payload = payPayload(
        txnId: intent.txnId,
        amount: amount,
        payee: payee,
        nonce: nonce,
        deviceId: deviceId,
        keyId: _requireKey(),
        now: _clock(),
      );
      _phase('Confirm with your fingerprint');
      final signature = await chip.sign(
        payload,
        title: 'Pay ${rupees(amount)}',
        subtitle: 'to $payee',
      );
      _say(LogKind.ok, 'The chip signed ${intent.txnId}.');
      return Authorisation.ready(
        PaymentDraft(
          amount: amount,
          payee: payee,
          txnId: intent.txnId,
          payload: payload,
          signature: signature,
        ),
      );
    } on Exception catch (e) {
      final failure = await _failed(e, steppedUp: steppedUp, txnId: txnId);
      history.insert(0, Payment(amount, payee, failure));
      return Authorisation.failed(failure);
    } finally {
      busy = false;
      phase = '';
      notifyListeners();
    }
  }

  /// Act 3, second half. Sends the payment for real, after the PIN.
  Future<PayResult> complete(PaymentDraft draft) async {
    if (busy) {
      return const PayResult.failed(
        'busy',
        'Another request is still running.',
      );
    }
    busy = true;
    notifyListeners();
    late PayResult result;
    try {
      var payload = draft.payload;
      if (tamper && payload != null) {
        payload = rewritePayee(payload, 'evil@upi');
        _say(
          LogKind.bad,
          'ATTACK: payee rewritten to evil@upi after the chip signed',
        );
      }
      _phase(
        draft.steppedUp ? 'Verifying the signature' : 'Sending the payment',
      );
      balance = await backend.confirm(
        draft.txnId,
        payload: payload,
        signature: draft.signature,
      );
      _say(
        LogKind.ok,
        draft.steppedUp
            ? 'Signed transaction accepted. Paid. Balance ${rupees(balance!)}'
            : 'Paid on the X-Token alone. Balance ${rupees(balance!)}',
      );
      result = PayResult.paid(
        txnId: draft.txnId,
        balance: balance!,
        steppedUp: draft.steppedUp,
      );
    } on Exception catch (e) {
      result = await _failed(e, steppedUp: draft.steppedUp, txnId: draft.txnId);
    } finally {
      busy = false;
      phase = '';
      notifyListeners();
    }
    history.insert(0, Payment(draft.amount, draft.payee, result));
    notifyListeners();
    return result;
  }

  /// The user backed out at the PIN screen. The backend's pending
  /// transaction and nonce simply expire.
  void abandon(PaymentDraft draft) {
    _say(
      LogKind.warn,
      'Payment ${draft.txnId} cancelled at the PIN screen. Nothing was sent.',
    );
  }

  /// Both halves in one call, for callers with no PIN screen in between.
  Future<PayResult> pay(int amount, String payee) async {
    final authorisation = await authorise(amount, payee);
    final draft = authorisation.draft;
    return draft == null ? authorisation.failure! : complete(draft);
  }

  Future<PayResult> _failed(
    Exception e, {
    required bool steppedUp,
    String? txnId,
  }) async {
    final code = switch (e) {
      ApiException(:final code) => code,
      ChipException(:final code) => code,
      SocketException() => 'unreachable',
      TimeoutException() => 'timeout',
      _ => 'error',
    };
    if (code == 'KEY_INVALIDATED') {
      status = await chip.status();
      backend.dropToken();
    }
    if (code == 'token_invalid') backend.dropToken();
    final message = _explain(e);
    _say(LogKind.bad, message);
    return PayResult.failed(code, message, steppedUp: steppedUp, txnId: txnId);
  }

  // ---- housekeeping --------------------------------------------------------

  void logout() {
    backend.dropToken();
    balance = null;
    otpOnlySession = false;
    stage = LoginStage.idle;
    history.clear();
    notifyListeners();
  }

  /// Deletes the hardware key and the session.
  Future<void> reset() async {
    await chip.delete();
    backend.dropToken();
    verdict = null;
    balance = null;
    otpOnlySession = false;
    stage = LoginStage.idle;
    incoming = null;
    history.clear();
    status = await chip.status();
    _say(LogKind.step, 'Device key deleted. Log in to bind this phone again.');
  }

  String _masked() =>
      phone.length < 4 ? phone : '••${phone.substring(phone.length - 4)}';

  String _requireKey() {
    // Prefer the id the backend derived at enrolment. After an app restart
    // only the phone's own derivation is left, and the two should agree.
    final keyId = verdict?.keyId ?? status.keyId;
    if (!status.enrolled || keyId == null) {
      throw const ChipException(
        'NO_KEY',
        'This phone has no device key. Log in again to bind it.',
      );
    }
    return keyId;
  }

  /// Runs one user action, turning any failure into [error].
  Future<void> _guarded(Future<void> Function() body) async {
    if (busy) return;
    busy = true;
    error = null;
    notifyListeners();
    try {
      await body();
    } on Exception catch (e) {
      error = _explain(e);
      _say(LogKind.bad, error!);
    } finally {
      busy = false;
      phase = '';
      notifyListeners();
    }
  }

  String _explain(Exception e) => switch (e) {
    ChipException(code: 'KEY_INVALIDATED') =>
      'The device key was invalidated by a biometric or lock screen change. Log in again.',
    ChipException(code: 'CANCELLED') =>
      'Fingerprint cancelled. Nothing was signed.',
    ChipException(:final code, :final message) =>
      message.isEmpty ? code : message,
    ApiException(code: 'bad_signature') =>
      'The backend rejected the signature: what it received is not what this phone signed.',
    ApiException(code: 'token_invalid') =>
      'Your session expired. Log in again.',
    ApiException(code: 'otp_invalid') => 'That code is wrong or has expired.',
    ApiException(:final code, :final message) =>
      message.isEmpty ? code : message,
    SocketException() =>
      'Cannot reach the backend. Is the verifier running, and did you run "adb reverse tcp:7447 tcp:7447"?',
    TimeoutException() => 'The backend did not answer in time.',
    _ => e.toString(),
  };
}
